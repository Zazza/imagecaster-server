package train

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModes_DisplayChangedDuringContinueKept(t *testing.T) {
	e := newEnv(t)
	writePersonDisplay(t, e.cfg.LoraDir, "anna", baseZImage, "Аня")
	rec := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	e.waitStarted()
	d := e.setDisplay("anna", "Анна", false)
	require.Equal(t, http.StatusOK, d.Code, d.Body.String())
	e.finishJob(decodeJob(t, rec).ID())
	assert.Equal(t, "Анна", e.card("anna")["display_name"])
}

func TestModes_RetrainKeepsIdentityWhenNotRebuilt(t *testing.T) {
	e := newEnv(t)
	e.m.SetIdentityBuilder(&fakeIdentity{available: true})
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	require.Equal(t, http.StatusOK, e.postIdentity("anna", 3).Code)
	e.m.SetIdentityBuilder(&fakeIdentity{available: false})
	e.trainMode(modeFields("anna", "woman", "retrain"))
	assert.NotNil(t, e.card("anna")["identity"])
}

func TestModes_PhotosAtFromForm(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	at := time.Date(2026, 11, 20, 10, 0, 0, 123000000, time.UTC)
	f := modeFields("anna", "woman", "continue")
	f["photos_at"] = at.Format(time.RFC3339Nano)
	e.trainMode(f)
	assert.True(t, at.Equal(parseTime(t, e.card("anna")["photos_at"])))

	bad := modeFields("anna", "woman", "continue")
	bad["photos_at"] = "yesterday"
	rec := e.post(bad, photos(t, 10))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestModes_FailedContinueKeepsRealPrevious(t *testing.T) {
	e := newEnv(t)
	writePerson(t, e.cfg.LoraDir, "anna", "woman", baseZImage, 10)
	e.trainMode(modeFields("anna", "woman", "continue"))
	require.True(t, e.previousOf("anna"))
	prev, err := os.ReadFile(e.m.prevLora("anna"))
	require.NoError(t, err)
	require.Equal(t, "lora-anna", string(prev))

	rec := e.post(modeFields("anna", "woman", "continue"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)
	e.waitStarted()
	e.finishRun(runResult{err: fmt.Errorf("boom")})
	e.waitStatus(j.ID(), "failed")
	e.m.Wait()

	prev, err = os.ReadFile(e.m.prevLora("anna"))
	require.NoError(t, err)
	assert.Equal(t, "lora-anna", string(prev))
	pending, _ := filepath.Glob(filepath.Join(e.m.prevDir(), "*.pending"))
	assert.Empty(t, pending)
}
