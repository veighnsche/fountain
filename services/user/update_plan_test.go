package user

import (
	"context"
	"testing"

	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"

	"github.com/stretchr/testify/require"
)

func TestUpdateUserPreferenceUsesOnlyItsPlannedColumnsWhileAuthorityIsBusy(t *testing.T) {
	unittest.PrepareTestEnv(t)
	claim, err := nativeoperation_model.ClaimOrdinary(t.Context(), "ord:authority/test/abc123", `{"kind":"ordinary","family":"authority"}`, "v")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, nativeoperation_model.ReleaseOwner(context.WithoutCancel(t.Context()), claim.Owner))
	})

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	originalAdmin := user.IsAdmin
	user.IsAdmin = !originalAdmin
	require.NoError(t, UpdateUser(db.DefaultContext, user, &UpdateOptions{Theme: optional.Some("planned-theme")}))

	fresh := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: user.ID})
	require.Equal(t, "planned-theme", fresh.Theme)
	require.Equal(t, originalAdmin, fresh.IsAdmin)
}

func TestEmptyUserUpdateDoesNotPromoteAnImplicitAllColumnWrite(t *testing.T) {
	unittest.PrepareTestEnv(t)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	originalAdmin, originalName := user.IsAdmin, user.FullName
	user.IsAdmin, user.FullName = !originalAdmin, "unrequested"

	require.NoError(t, UpdateUser(db.DefaultContext, user, &UpdateOptions{}))

	fresh := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: user.ID})
	require.Equal(t, originalAdmin, fresh.IsAdmin)
	require.Equal(t, originalName, fresh.FullName)
}
