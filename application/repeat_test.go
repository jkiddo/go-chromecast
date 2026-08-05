package application

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vishen/go-chromecast/cast"
)

func TestNewRepeatingQueueLoadUsesSingleItemRepeat(t *testing.T) {
	payload := newRepeatingQueueLoad(mediaItem{
		contentURL:  "http://192.0.2.1/video.mp4",
		contentType: "video/mp4",
	}, cast.RepeatModeSingle)

	require.Equal(t, cast.RepeatModeSingle, payload.RepeatMode)
	require.Len(t, payload.Items, 1)
	require.True(t, payload.Items[0].Autoplay)
	require.Equal(t, "http://192.0.2.1/video.mp4", payload.Items[0].Media.ContentId)

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "playbackDuration")
}

func TestNewRepeatingQueueLoadAllowsQueueRepeat(t *testing.T) {
	payload := newRepeatingQueueLoad(mediaItem{}, cast.RepeatModeAll)

	require.Equal(t, cast.RepeatModeAll, payload.RepeatMode)
}

func TestLoadRepeatingRejectsUnknownRepeatModeBeforeResolvingMedia(t *testing.T) {
	app := NewApplication()

	err := app.LoadRepeatingWithMode("does-not-exist.mp4", "video/mp4", false, "REPEAT_SOMETHING")

	require.EqualError(t, err, `unsupported repeat mode "REPEAT_SOMETHING"`)
}
