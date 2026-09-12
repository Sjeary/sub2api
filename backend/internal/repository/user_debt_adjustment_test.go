package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAdjustBalance_AllowsPartialDebtRepayment(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectQuery(`UPDATE users SET balance = balance \+ \$1, updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL AND \(\$1::numeric > 0 OR balance \+ \$1 >= 0\) RETURNING balance - \$1, balance`).
		WithArgs(0.3, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"old", "new"}).AddRow(-10.0, -9.7))
	change, err := repo.AdjustBalance(context.Background(), 42, 0.3)
	require.NoError(t, err)
	require.Equal(t, -10.0, change.Old)
	require.Equal(t, -9.7, change.New)
	require.NoError(t, mock.ExpectationsWereMet())
}
