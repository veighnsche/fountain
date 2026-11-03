// Copyright 2019 The Gitea Authors.
// All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/git/pipeline"
	"forgejo.org/modules/lfs"
)

// LFSPush verifies shared-store content and associates new source LFS pointers
// with the target repository. Failures stop before the caller's Git push.
// beforeWrite rechecks a conditional caller's current authority after storage I/O.
func LFSPush(ctx context.Context, tmpBasePath, mergeHeadSHA, mergeBaseSHA string, pr *issues_model.PullRequest, beforeWrite func() error) error {
	// Stream candidate objects through native Git, inspect pointer-sized blobs,
	// then verify source/content evidence before associating them with the target.
	revListReader, revListWriter := io.Pipe()
	shasToCheckReader, shasToCheckWriter := io.Pipe()
	catFileCheckReader, catFileCheckWriter := io.Pipe()
	shasToBatchReader, shasToBatchWriter := io.Pipe()
	catFileBatchReader, catFileBatchWriter := io.Pipe()
	errChan := make(chan error, 2)
	wg := sync.WaitGroup{}
	wg.Add(6)
	// Create the go-routines in reverse order.

	// 6. Take the output of cat-file --batch and check if each file in turn
	// to see if they're pointers to files in the LFS store associated with
	// the head repo and add them to the base repo if so
	go func() {
		defer wg.Done()
		errChan <- createLFSMetaObjectsFromCatFileBatch(ctx, catFileBatchReader, pr, beforeWrite)
	}()

	// 5. Take the shas of the blobs and batch read them
	go pipeline.CatFileBatch(ctx, shasToBatchReader, catFileBatchWriter, &wg, tmpBasePath)

	// 4. From the provided objects restrict to blobs <=1k
	go pipeline.BlobsLessThan1024FromCatFileBatchCheck(catFileCheckReader, shasToBatchWriter, &wg)

	// 3. Run batch-check on the objects retrieved from rev-list
	go pipeline.CatFileBatchCheck(ctx, shasToCheckReader, catFileCheckWriter, &wg, tmpBasePath)

	// 2. Check each object retrieved rejecting those without names as they will be commits or trees
	go pipeline.BlobsFromRevListObjects(revListReader, shasToCheckWriter, &wg)

	// 1. Run rev-list objects from mergeHead to mergeBase
	go pipeline.RevListObjects(ctx, revListWriter, &wg, tmpBasePath, mergeHeadSHA, mergeBaseSHA, errChan)

	wg.Wait()
	close(errChan)
	var result error
	for err := range errChan {
		result = errors.Join(result, err)
	}
	return errors.Join(result, ctx.Err())
}

func createLFSMetaObjectsFromCatFileBatch(ctx context.Context, catFileBatchReader *io.PipeReader, pr *issues_model.PullRequest, beforeWrite func() error) (result error) {
	defer func() { _ = catFileBatchReader.CloseWithError(result) }()

	contentStore := lfs.NewContentStore()

	bufferedReader := bufio.NewReader(catFileBatchReader)
	buf := make([]byte, 1025)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := bufferedReader.ReadString('\n')
		if errors.Is(err, io.EOF) && header == "" {
			return nil
		}
		if err != nil {
			return err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return fmt.Errorf("invalid LFS scan object header")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > 1024 {
			return fmt.Errorf("invalid LFS scan object size")
		}
		pointerBuf := buf[:size+1]
		if _, err := io.ReadFull(bufferedReader, pointerBuf); err != nil {
			return err
		}
		if pointerBuf[size] != '\n' {
			return fmt.Errorf("invalid LFS scan object terminator")
		}
		pointerBuf = pointerBuf[:size]
		// Now we need to check if the pointerBuf is an LFS pointer
		pointer, _ := lfs.ReadPointerFromBuffer(pointerBuf)
		if !pointer.IsValid() {
			continue
		}

		checkSource := func(ctx context.Context) error {
			meta, err := git_model.GetLFSMetaObjectByOid(ctx, pr.HeadRepoID, pointer.Oid)
			if err != nil {
				return err
			}
			if meta.Size != pointer.Size {
				return lfs.ErrSizeMismatch
			}
			return nil
		}
		if err := checkSource(ctx); err != nil {
			return err
		}
		verified, err := contentStore.Verify(pointer)
		if err != nil {
			return err
		}
		if !verified {
			return fmt.Errorf("LFS content %s is missing or has the wrong size", pointer.Oid)
		}
		if beforeWrite != nil {
			if err := beforeWrite(); err != nil {
				return err
			}
		}
		// Preserve the caller's execution/cancellation context. Re-read source
		// association after storage I/O, and order the target write under the
		// existing held reservation without holding SQL across that I/O.
		if err := db.WithTx(ctx, func(tx context.Context) error {
			if err := nativeoperation_model.RequireHeldOwnership(tx); err != nil {
				return err
			}
			if err := checkSource(tx); err != nil {
				return err
			}
			meta, err := git_model.NewLFSMetaObject(tx, pr.BaseRepoID, pointer)
			if err != nil {
				return err
			}
			if meta.Size != pointer.Size {
				return lfs.ErrSizeMismatch
			}
			return nil
		}); err != nil {
			return err
		}
	}
}
