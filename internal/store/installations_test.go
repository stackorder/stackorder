//go:build integration

package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuspendInstallationOrder(t *testing.T) {
	f := newFixture(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	t1 := base
	t2 := base.Add(time.Second)
	t3 := base.Add(2 * time.Second)

	applied, err := f.s.SuspendInstallation(f.ctx, 1, true, t2)
	require.NoError(t, err)
	assert.True(t, applied, "the first observation applies")
	got, err := f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, got.SuspendedAt)

	applied, err = f.s.SuspendInstallation(f.ctx, 1, false, t1)
	require.NoError(t, err)
	assert.False(t, applied, "an observation from earlier is ignored")
	got, err = f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.NotNil(t, got.SuspendedAt, "the installation stays suspended")

	applied, err = f.s.SuspendInstallation(f.ctx, 1, false, t3)
	require.NoError(t, err)
	assert.True(t, applied, "a later observation wins")
	got, err = f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.Nil(t, got.SuspendedAt)

	applied, err = f.s.SuspendInstallation(f.ctx, 404, true, t3)
	require.NoError(t, err)
	assert.False(t, applied, "an unknown installation is a no-op, not an error")
}

func TestSuspendInstallationReconcileWinsOverEarlierDelivery(t *testing.T) {
	f := newFixture(t)
	observed, err := f.s.Now(f.ctx)
	require.NoError(t, err)
	past := observed.Add(-time.Hour)

	applied, err := f.s.SuspendInstallation(f.ctx, 1, false, observed)
	require.NoError(t, err)
	require.True(t, applied, "the reconcile records the current state")

	applied, err = f.s.SuspendInstallation(f.ctx, 1, true, past)
	require.NoError(t, err)
	assert.False(t, applied, "a delivery received before the reconcile is ignored")
	got, err := f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	assert.Nil(t, got.SuspendedAt)
}

func TestSuspendInstallationConcurrent(t *testing.T) {
	f := newFixture(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	const claimers = 64

	var wg sync.WaitGroup
	errs := make([]error, claimers)
	for i := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := base.Add(time.Duration(i) * time.Millisecond)
			_, errs[i] = f.s.SuspendInstallation(f.ctx, 1, i%2 == 0, at)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	got, err := f.s.GetInstallation(f.ctx, 1)
	require.NoError(t, err)
	wantSuspended := (claimers-1)%2 == 0
	if wantSuspended {
		assert.NotNil(t, got.SuspendedAt, "the latest observation suspends, whatever order the claimers ran")
	} else {
		assert.Nil(t, got.SuspendedAt, "the latest observation unsuspends, whatever order the claimers ran")
	}
}
