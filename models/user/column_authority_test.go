package user_test

import (
	"testing"

	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/require"
)

func TestUserColumnAuthorityKeepsTelemetryExemptionAndFencesUnknownColumns(t *testing.T) {
	require.False(t, user_model.UserColumnsRequireAuthority("theme", "last_login_unix"))
	require.True(t, user_model.UserColumnsRequireAuthority("theme", "is_admin"))
	require.True(t, user_model.UserColumnsRequireAuthority("new_permission_field"))
	require.True(t, user_model.UserColumnsRequireAuthority())
}
