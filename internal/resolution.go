package internal

import playbackv1 "github.com/Muxcore-Media/playback-contract/proto/playbackv1"

func streamResolutionFromPlexSession(s plexSession) string {
	return playbackv1.NormalizeStreamResolution(s.Height, s.Width, "")
}

func streamResolutionFromPayload(ev playbackEventPayload) string {
	if ev.StreamResolution != "" {
		return ev.StreamResolution
	}
	return playbackv1.NormalizeStreamResolution(ev.VideoHeight, ev.VideoWidth, "")
}
