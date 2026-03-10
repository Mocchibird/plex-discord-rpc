package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRuntimeConfigUsesDefaultSocketForDebugMode(t *testing.T) {
	t.Parallel()

	cfg := parseRuntimeConfig([]string{"plex-discord-rpc.exe", "--print-artwork-url"})
	if !cfg.printArtworkOnly {
		t.Fatal("printArtworkOnly = false, want true")
	}
	if cfg.socketPath != defaultSocketPath() {
		t.Fatalf("socketPath = %q, want %q", cfg.socketPath, defaultSocketPath())
	}
}

func TestParseRuntimeConfigPreservesPositionalArgs(t *testing.T) {
	t.Parallel()

	cfg := parseRuntimeConfig([]string{"plex-discord-rpc.exe", "--print-artwork-url", `\\.\pipe\custom`, "client-id"})
	if cfg.socketPath != `\\.\pipe\custom` {
		t.Fatalf("socketPath = %q, want %q", cfg.socketPath, `\\.\pipe\custom`)
	}
	if cfg.clientID != "client-id" {
		t.Fatalf("clientID = %q, want %q", cfg.clientID, "client-id")
	}
}

func TestParseRuntimeConfigEnablesTokenizedArtworkTestMode(t *testing.T) {
	t.Parallel()

	cfg := parseRuntimeConfig([]string{"plex-discord-rpc.exe", "--test-tokenized-artwork-url"})
	if !cfg.testTokenizedArtwork {
		t.Fatal("testTokenizedArtwork = false, want true")
	}
	if cfg.socketPath != defaultSocketPath() {
		t.Fatalf("socketPath = %q, want %q", cfg.socketPath, defaultSocketPath())
	}
}

func TestIsSafeDiscordArtworkURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "metadata static url is allowed",
			raw:  "https://metadata-static.plex.tv/p/12345/poster.jpg",
			want: true,
		},
		{
			name: "tmdb provider url is allowed",
			raw:  "https://image.tmdb.org/t/p/original/poster.jpg",
			want: true,
		},
		{
			name: "tokenized url is blocked",
			raw:  "https://metadata-static.plex.tv/p/12345/poster.jpg?X-Plex-Token=secret",
			want: false,
		},
		{
			name: "relative server path is blocked",
			raw:  "/library/metadata/10/thumb/1715112805",
			want: false,
		},
		{
			name: "local server url is blocked",
			raw:  "https://127.0.0.1:32400/library/metadata/10/thumb/1715112805",
			want: false,
		},
		{
			name: "plex direct host is blocked",
			raw:  "https://46-126-209-158.example.plex.direct:32400/photo/:/transcode?url=%2Flibrary%2Fmetadata%2F10%2Fthumb",
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isSafeDiscordArtworkURL(tt.raw); got != tt.want {
				t.Fatalf("isSafeDiscordArtworkURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestMetadataImagesEndpoints(t *testing.T) {
	t.Parallel()

	got, err := metadataImagesEndpoints(
		"movie",
		"/library/metadata/123",
		"",
		"",
		"/library/metadata/123/thumb/1715112805",
		"",
		"",
		"https://192-168-1-92.example.plex.direct:32400/library/parts/999/file.mp4?includeExternalMedia=1",
	)
	if err != nil {
		t.Fatalf("metadataImagesEndpoints() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(metadataImagesEndpoints()) = %d, want 2", len(got))
	}
	if got[0] != "https://192-168-1-92.example.plex.direct:32400/library/metadata/123/images" {
		t.Fatalf("metadataImagesEndpoints()[0] = %q, want %q", got[0], "https://192-168-1-92.example.plex.direct:32400/library/metadata/123/images")
	}
	if got[1] != "http://192.168.1.92:32400/library/metadata/123/images" {
		t.Fatalf("metadataImagesEndpoints()[1] = %q, want %q", got[1], "http://192.168.1.92:32400/library/metadata/123/images")
	}
}

func TestBuildLegacyTokenizedArtworkURL(t *testing.T) {
	t.Parallel()

	got := buildLegacyTokenizedArtworkURL(
		"movie",
		"/library/metadata/123/thumb/1715112805",
		"",
		"",
		"https://192-168-1-92.example.plex.direct:32400/library/parts/999/file.mp4?includeExternalMedia=1",
		"secret",
	)
	want := "http://192.168.1.92:32400/library/metadata/123/thumb/1715112805?X-Plex-Token=secret"
	if got != want {
		t.Fatalf("buildLegacyTokenizedArtworkURL() = %q, want %q", got, want)
	}
}

func TestSelectSafeArtworkURLMovie(t *testing.T) {
	t.Parallel()

	got := selectSafeArtworkURL(
		"movie",
		"https://metadata-static.plex.tv/p/12345/poster.jpg",
		"",
		"",
	)
	want := "https://metadata-static.plex.tv/p/12345/poster.jpg"
	if got != want {
		t.Fatalf("selectSafeArtworkURL() = %q, want %q", got, want)
	}
}

func TestSelectSafeArtworkURLEpisodeFallsThroughCandidates(t *testing.T) {
	t.Parallel()

	got := selectSafeArtworkURL(
		"episode",
		"https://image.tmdb.org/t/p/original/episode.jpg",
		"/library/metadata/10/thumb/1715112805",
		"https://metadata-static.plex.tv/p/show/poster.jpg",
	)
	want := "https://metadata-static.plex.tv/p/show/poster.jpg"
	if got != want {
		t.Fatalf("selectSafeArtworkURL() = %q, want %q", got, want)
	}
}

func TestFetchMetadataProviderArtworkURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/metadata/123/images" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/library/metadata/123/images")
		}
		if got := r.Header.Get("X-Plex-Token"); got != "secret" {
			t.Fatalf("X-Plex-Token = %q, want %q", got, "secret")
		}
		if got := r.Header.Get("X-Plex-Client-Identifier"); got != "client-id" {
			t.Fatalf("X-Plex-Client-Identifier = %q, want %q", got, "client-id")
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"MediaContainer":{"Image":[{"type":"background","url":"https://image.tmdb.org/t/p/original/background.jpg"},{"type":"coverPoster","url":"https://image.tmdb.org/t/p/original/poster.jpg"}]}}`)
	}))
	defer server.Close()

	got, cacheable := fetchMetadataProviderArtworkURL(server.Client(), "movie", "/library/metadata/123", "", "", "/library/metadata/123/thumb/1715112805", "", "", server.URL+"/library/parts/999/file.mp4", "secret", "client-id")
	if !cacheable {
		t.Fatal("fetchMetadataProviderArtworkURL() cacheable = false, want true")
	}
	want := "https://image.tmdb.org/t/p/original/poster.jpg"
	if got != want {
		t.Fatalf("fetchMetadataProviderArtworkURL() = %q, want %q", got, want)
	}
}

func TestFetchMetadataProviderArtworkURLRejectsUnsafeReturnedURLs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"MediaContainer":{"Image":[{"type":"coverPoster","url":"https://46-126-209-158.example.plex.direct:32400/photo/:/transcode?url=%2Flibrary%2Fmetadata%2F10%2Fthumb"}]}}`)
	}))
	defer server.Close()

	got, cacheable := fetchMetadataProviderArtworkURL(server.Client(), "movie", "/library/metadata/123", "", "", "/library/metadata/123/thumb/1715112805", "", "", server.URL+"/library/parts/999/file.mp4", "secret", "client-id")
	if !cacheable {
		t.Fatal("fetchMetadataProviderArtworkURL() cacheable = false, want true")
	}
	if got != "" {
		t.Fatalf("fetchMetadataProviderArtworkURL() = %q, want empty string", got)
	}
}

func TestBuildActivityUsesFallbackArtworkWhenThumbMissing(t *testing.T) {
	t.Parallel()

	activity := buildActivity(mediaInfo{
		title:     "Local Movie",
		mediaType: "movie",
		paused:    true,
	})

	if activity.LargeImageKey != largeImageKey {
		t.Fatalf("LargeImageKey = %q, want %q", activity.LargeImageKey, largeImageKey)
	}
	if activity.SmallImageKey != imagePause {
		t.Fatalf("SmallImageKey = %q, want %q", activity.SmallImageKey, imagePause)
	}
	if activity.SmallImageText != "Paused" {
		t.Fatalf("SmallImageText = %q, want %q", activity.SmallImageText, "Paused")
	}
	if activity.Timestamps != nil {
		t.Fatalf("Timestamps = %#v, want nil", activity.Timestamps)
	}
}

func TestBuildActivityIdle(t *testing.T) {
	t.Parallel()

	activity := buildActivity(mediaInfo{idle: true})

	if activity.LargeImageKey != largeImageKey {
		t.Fatalf("LargeImageKey = %q, want %q", activity.LargeImageKey, largeImageKey)
	}
	if activity.SmallImageKey != imageIdle {
		t.Fatalf("SmallImageKey = %q, want %q", activity.SmallImageKey, imageIdle)
	}
	if activity.Details != "Nothing Playing" {
		t.Fatalf("Details = %q, want %q", activity.Details, "Nothing Playing")
	}
}

func TestBuildActivityEpisodeWithSafeArtworkAndTimestamps(t *testing.T) {
	t.Parallel()

	activity := buildActivity(mediaInfo{
		title:       "The Show",
		showTitle:   "Pilot",
		showSeason:  1,
		showEpisode: 2,
		mediaType:   "episode",
		thumbURL:    "https://metadata-static.plex.tv/p/show/poster.jpg",
		timePos:     30,
		duration:    120,
	})

	if activity.LargeImageKey != "https://metadata-static.plex.tv/p/show/poster.jpg" {
		t.Fatalf("LargeImageKey = %q, want safe artwork URL", activity.LargeImageKey)
	}
	if activity.State != "S01E02 - Pilot" {
		t.Fatalf("State = %q, want %q", activity.State, "S01E02 - Pilot")
	}
	if activity.SmallImageKey != imagePlay {
		t.Fatalf("SmallImageKey = %q, want %q", activity.SmallImageKey, imagePlay)
	}
	if activity.Timestamps == nil {
		t.Fatal("Timestamps = nil, want non-nil")
	}
	if got, want := activity.Timestamps.End-activity.Timestamps.Start, int64(120000); got != want {
		t.Fatalf("timestamp duration = %d, want %d", got, want)
	}
}
