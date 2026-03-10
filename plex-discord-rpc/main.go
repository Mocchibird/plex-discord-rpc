package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"plex-discord-rpc/discordrpc"
	"plex-discord-rpc/mpvipc"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	largeImageKey     = "logo"
	largeImageText    = "Plex"
	defaultDiscordCID = "1476395038555640050"

	imagePlay  = "play"
	imagePause = "pause"
	imageIdle  = "stop"

	pollInterval             = time.Second
	retryInterval            = 500 * time.Millisecond
	tokenizedArtworkTestHold = 20 * time.Second
	discordOpenRetryInterval = 500 * time.Millisecond
	discordOpenRetryTimeout  = 10 * time.Second
)

type runtimeConfig struct {
	socketPath           string
	clientID             string
	printArtworkOnly     bool
	testTokenizedArtwork bool
}

type plexMediaItem struct {
	URL      string `json:"url"`
	Type     string `json:"type"`
	Decision struct {
		MetadataItem struct {
			Title            string `json:"title"`
			Key              string `json:"key"`
			Thumb            string `json:"thumb"`
			Index            int    `json:"index"`
			Type             string `json:"type"`
			IsAdult          bool   `json:"isAdult"`
			GrandparentTitle string `json:"grandparentTitle"`
			GrandparentKey   string `json:"grandparentKey"`
			GrandparentThumb string `json:"grandparentThumb"`
			ParentKey        string `json:"parentKey"`
			ParentThumb      string `json:"parentThumb"`
			ParentIndex      int    `json:"parentIndex"`
		} `json:"metadataItem"`
	} `json:"decision"`
}

type plexMetadataImagesResponse struct {
	MediaContainer struct {
		Image []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"Image"`
	} `json:"MediaContainer"`
}

type mediaInfo struct {
	title          string
	contentType    string
	mediaType      string
	showTitle      string
	showSeason     int
	showEpisode    int
	thumbURL       string
	legacyThumbURL string
	paused         bool
	idle           bool
	timePos        float64
	duration       float64
}

var (
	client                    *mpvipc.Client
	presence                  *discordrpc.Presence
	loggedArtworkBlockReasons = map[string]struct{}{}
	loggedArtworkBlockMu      sync.Mutex
	metadataArtworkCache      = map[string]string{}
	metadataArtworkCacheMu    sync.Mutex
	metadataArtworkHTTPClient = &http.Client{Timeout: 3 * time.Second}
	config                    runtimeConfig
)

func defaultSocketPath() string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\plexdiscordsocket`
	}
	return "/tmp/plexdiscordsocket"
}

func parseRuntimeConfig(args []string) runtimeConfig {
	cfg := runtimeConfig{socketPath: defaultSocketPath()}
	var positional []string

	for _, arg := range args[1:] {
		switch arg {
		case "--print-artwork-url":
			cfg.printArtworkOnly = true
		case "--test-tokenized-artwork-url":
			cfg.testTokenizedArtwork = true
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		cfg.socketPath = positional[0]
	}
	if len(positional) > 1 {
		cfg.clientID = positional[1]
	}

	return cfg
}

func debugArtworkf(format string, args ...interface{}) {
	if config.printArtworkOnly || config.testTokenizedArtwork {
		log.Printf(format, args...)
	}
}

func describeArtworkURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() {
		return raw
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func logBlockedArtworkReason(reason string) {
	if reason == "" {
		return
	}

	loggedArtworkBlockMu.Lock()
	if _, seen := loggedArtworkBlockReasons[reason]; seen {
		loggedArtworkBlockMu.Unlock()
		return
	}
	loggedArtworkBlockReasons[reason] = struct{}{}
	loggedArtworkBlockMu.Unlock()

	log.Printf("Skipping Plex artwork for Discord: %s", reason)
}

func isPublicArtworkHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if strings.HasSuffix(host, ".plex.direct") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() && !ip.IsUnspecified()
	}

	return strings.Contains(host, ".")
}

func blockedArtworkReason(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "artwork URL was empty"
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "artwork URL could not be parsed"
	}
	if !parsed.IsAbs() {
		return "artwork URL was not an absolute public URL"
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "artwork URL was not https"
	}
	if parsed.User != nil {
		return "artwork URL contained unexpected credentials"
	}
	if strings.Contains(strings.ToLower(parsed.RawQuery), "x-plex-token=") {
		return "artwork URL contained an X-Plex-Token and was intentionally blocked"
	}
	if !isPublicArtworkHost(parsed.Hostname()) {
		return "artwork URL was not a public host"
	}

	return ""
}

func isSafeDiscordArtworkURL(raw string) bool {
	return blockedArtworkReason(raw) == ""
}

func selectSafeArtworkURL(mediaType, thumb, parentThumb, grandparentThumb string) string {
	var candidates []string
	switch mediaType {
	case "episode":
		candidates = []string{parentThumb, grandparentThumb, thumb}
	default:
		candidates = []string{thumb}
	}

	debugArtworkf(
		"Direct artwork candidates: parent=%q grandparent=%q thumb=%q",
		describeArtworkURL(parentThumb),
		describeArtworkURL(grandparentThumb),
		describeArtworkURL(thumb),
	)

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if isSafeDiscordArtworkURL(candidate) {
			debugArtworkf("Using direct public artwork URL: %s", candidate)
			return candidate
		}
		debugArtworkf("Rejected direct artwork candidate %q: %s", describeArtworkURL(candidate), blockedArtworkReason(candidate))
		// Discord supports external image URLs, but tokenized or server-local artwork
		// would leak private server access and must never be sent.
		logBlockedArtworkReason(blockedArtworkReason(candidate))
	}

	return ""
}

func selectLegacyArtworkPath(mediaType, thumb, parentThumb, grandparentThumb string) string {
	switch mediaType {
	case "episode":
		if parentThumb != "" {
			return parentThumb
		}
		if grandparentThumb != "" {
			return grandparentThumb
		}
		return thumb
	default:
		return thumb
	}
}

func legacyArtworkBaseURL(mediaURL string) string {
	parsed, err := url.Parse(mediaURL)
	if err != nil || !parsed.IsAbs() {
		return ""
	}

	host := parsed.Hostname()
	if host == "" {
		return ""
	}

	if decodedIP := decodePlexDirectHost(host); decodedIP != "" {
		host = decodedIP
		parsed.Scheme = "http"
	} else if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		parsed.Scheme = "http"
	}

	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return strings.TrimRight(parsed.String(), "/")
}

func buildLegacyTokenizedArtworkURL(mediaType, thumb, parentThumb, grandparentThumb, mediaURL, plexToken string) string {
	if strings.TrimSpace(plexToken) == "" {
		return ""
	}

	candidate := selectLegacyArtworkPath(mediaType, thumb, parentThumb, grandparentThumb)
	if strings.TrimSpace(candidate) == "" {
		return ""
	}

	baseURL := legacyArtworkBaseURL(mediaURL)
	if baseURL == "" {
		return ""
	}

	parsedCandidate, err := url.Parse(candidate)
	if err != nil {
		return ""
	}
	path := parsedCandidate.Path
	if path == "" {
		path = candidate
	}
	if !strings.HasPrefix(path, "/library/metadata/") {
		return ""
	}

	query := parsedCandidate.Query()
	query.Del("X-Plex-Token")
	query.Set("X-Plex-Token", plexToken)

	legacyURL := baseURL + path
	if encoded := query.Encode(); encoded != "" {
		legacyURL += "?" + encoded
	}
	return legacyURL
}

func metadataPathFromCandidate(candidate string) string {
	if strings.TrimSpace(candidate) == "" {
		return ""
	}

	path := candidate
	if parsed, err := url.Parse(candidate); err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/library/metadata/") {
		return ""
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 {
		return ""
	}

	return "/" + strings.Join(parts[:3], "/")
}

func decodePlexDirectHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if !strings.HasSuffix(host, ".plex.direct") {
		return ""
	}

	labels := strings.Split(host, ".")
	if len(labels) == 0 {
		return ""
	}

	candidateIP := strings.ReplaceAll(labels[0], "-", ".")
	ip := net.ParseIP(candidateIP)
	if ip == nil || !ip.IsPrivate() {
		return ""
	}

	return ip.String()
}

func metadataImagesEndpoints(mediaType, key, parentKey, grandparentKey, thumb, parentThumb, grandparentThumb, mediaURL string) ([]string, error) {
	parsed, err := url.Parse(mediaURL)
	if err != nil {
		return nil, err
	}
	if !parsed.IsAbs() {
		return nil, errors.New("metadata URL was not absolute")
	}

	var candidates []string
	switch mediaType {
	case "episode":
		candidates = []string{parentKey, grandparentKey, key, parentThumb, grandparentThumb, thumb, parsed.Path}
	default:
		candidates = []string{key, thumb, parsed.Path}
	}

	for _, candidate := range candidates {
		metadataPath := metadataPathFromCandidate(candidate)
		if metadataPath == "" {
			continue
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Path = metadataPath + "/images"

		endpoints := []string{parsed.String()}
		if decodedIP := decodePlexDirectHost(parsed.Hostname()); decodedIP != "" {
			fallback := *parsed
			fallback.Scheme = "http"
			fallback.Host = decodedIP
			if port := parsed.Port(); port != "" {
				fallback.Host = net.JoinHostPort(decodedIP, port)
			}
			endpoints = append(endpoints, fallback.String())
		}
		return endpoints, nil
	}

	return nil, errors.New("metadata URL was missing a metadata item path")
}

func selectPreferredMetadataImageURL(images []struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}) string {
	preferredTypes := []string{"coverPoster", "poster", "snapshot", "background"}
	for _, preferredType := range preferredTypes {
		for _, image := range images {
			if !strings.EqualFold(image.Type, preferredType) {
				continue
			}
			if isSafeDiscordArtworkURL(image.URL) {
				debugArtworkf("Using PMS /images artwork type %q: %s", image.Type, image.URL)
				return image.URL
			}
			debugArtworkf("Rejected PMS /images artwork type %q at %q: %s", image.Type, describeArtworkURL(image.URL), blockedArtworkReason(image.URL))
			logBlockedArtworkReason(blockedArtworkReason(image.URL))
		}
	}

	for _, image := range images {
		if isSafeDiscordArtworkURL(image.URL) {
			debugArtworkf("Using fallback PMS /images artwork type %q: %s", image.Type, image.URL)
			return image.URL
		}
		debugArtworkf("Rejected PMS /images fallback artwork type %q at %q: %s", image.Type, describeArtworkURL(image.URL), blockedArtworkReason(image.URL))
		logBlockedArtworkReason(blockedArtworkReason(image.URL))
	}

	debugArtworkf("PMS /images did not return a safe public artwork URL")
	return ""
}

func fetchMetadataProviderArtworkURL(httpClient *http.Client, mediaType, key, parentKey, grandparentKey, thumb, parentThumb, grandparentThumb, mediaURL, plexToken, plexClientID string) (string, bool) {
	if httpClient == nil || strings.TrimSpace(mediaURL) == "" || strings.TrimSpace(plexToken) == "" {
		return "", false
	}

	imagesURLs, err := metadataImagesEndpoints(mediaType, key, parentKey, grandparentKey, thumb, parentThumb, grandparentThumb, mediaURL)
	if err != nil {
		log.Println("metadata images URL error:", err)
		return "", false
	}

	for _, imagesURL := range imagesURLs {
		debugArtworkf("Querying PMS /images endpoint: %s", describeArtworkURL(imagesURL))

		req, err := http.NewRequest(http.MethodGet, imagesURL, nil)
		if err != nil {
			log.Println("metadata images request error:", err)
			continue
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Product", largeImageText)
		req.Header.Set("X-Plex-Token", plexToken)
		if strings.TrimSpace(plexClientID) != "" {
			req.Header.Set("X-Plex-Client-Identifier", plexClientID)
		}

		res, err := httpClient.Do(req)
		if err != nil {
			log.Println("metadata images request failed:", err)
			continue
		}

		var response plexMetadataImagesResponse
		if res.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			log.Printf("metadata images request returned %s", res.Status)
			continue
		}
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
			res.Body.Close()
			log.Println("metadata images decode error:", err)
			continue
		}
		res.Body.Close()

		artworkURL := selectPreferredMetadataImageURL(response.MediaContainer.Image)
		if artworkURL == "" {
			debugArtworkf("No safe artwork URL available from PMS /images for %s", describeArtworkURL(imagesURL))
		}
		return artworkURL, true
	}

	return "", false
}

func getCachedMetadataArtwork(mediaURL string) (string, bool) {
	metadataArtworkCacheMu.Lock()
	defer metadataArtworkCacheMu.Unlock()

	artworkURL, ok := metadataArtworkCache[mediaURL]
	return artworkURL, ok
}

func cacheMetadataArtwork(mediaURL, artworkURL string) {
	metadataArtworkCacheMu.Lock()
	metadataArtworkCache[mediaURL] = artworkURL
	metadataArtworkCacheMu.Unlock()
}

func selectArtworkForDiscord(mediaType, key, parentKey, grandparentKey, thumb, parentThumb, grandparentThumb, mediaURL, plexToken, plexClientID string) string {
	if directURL := selectSafeArtworkURL(mediaType, thumb, parentThumb, grandparentThumb); directURL != "" {
		return directURL
	}
	if strings.TrimSpace(mediaURL) == "" || strings.TrimSpace(plexToken) == "" {
		debugArtworkf("No media URL or Plex token available for PMS /images lookup; using static fallback")
		return ""
	}
	if cachedURL, ok := getCachedMetadataArtwork(mediaURL); ok {
		if cachedURL == "" {
			debugArtworkf("Using cached artwork decision: static fallback")
		} else {
			debugArtworkf("Using cached public artwork URL: %s", cachedURL)
		}
		return cachedURL
	}

	artworkURL, cacheable := fetchMetadataProviderArtworkURL(
		metadataArtworkHTTPClient,
		mediaType,
		key,
		parentKey,
		grandparentKey,
		thumb,
		parentThumb,
		grandparentThumb,
		mediaURL,
		plexToken,
		plexClientID,
	)
	if cacheable {
		cacheMetadataArtwork(mediaURL, artworkURL)
	}
	if artworkURL == "" {
		debugArtworkf("Final artwork decision: static fallback")
	} else {
		debugArtworkf("Final artwork decision: %s", artworkURL)
	}
	return artworkURL
}

func buildActivity(mediaInfo mediaInfo) (activity discordrpc.Activity) {
	activity.Type = 3

	// Idle
	if mediaInfo.idle {
		activity.SmallImageKey = imageIdle
		activity.LargeImageKey = largeImageKey
		activity.LargeImageText = largeImageText
		activity.Details = "Nothing Playing"
		activity.SmallImageText = "Nothing Playing"
		activity.State = ""
		return
	}

	// Large Image
	if mediaInfo.thumbURL != "" {
		activity.LargeImageKey = mediaInfo.thumbURL
	} else {
		activity.LargeImageKey = largeImageKey
	}

	// Details
	if mediaInfo.mediaType == "episode" && mediaInfo.showTitle != "" {
		activity.LargeImageText = mediaInfo.showTitle
		activity.Details = mediaInfo.title
	} else if mediaInfo.mediaType == "movie" && mediaInfo.title != "" {
		activity.LargeImageText = mediaInfo.title
		activity.Details = mediaInfo.title
	} else {
		activity.LargeImageText = largeImageText
		activity.Details = "Nothing Playing"
	}

	// State
	if mediaInfo.mediaType == "episode" && mediaInfo.showTitle != "" {
		activity.State = fmt.Sprintf("S%02dE%02d - %s", mediaInfo.showSeason, mediaInfo.showEpisode, mediaInfo.showTitle)
	} else if mediaInfo.mediaType == "movie" && mediaInfo.title != "" {
		activity.State = ""
	} else {
		activity.State = ""
	}

	// Small Image
	if mediaInfo.paused {
		activity.SmallImageKey = imagePause
		activity.SmallImageText = "Paused"
	} else {
		activity.SmallImageKey = imagePlay
		activity.SmallImageText = "Playing"
	}

	// Timestamps
	if !mediaInfo.paused && mediaInfo.duration > 0 && mediaInfo.timePos >= 0 {
		now := time.Now().UnixMilli()
		startTimePos := now - (int64(mediaInfo.timePos) * 1000)
		duration := startTimePos + (int64(mediaInfo.duration) * 1000)
		activity.Timestamps = &discordrpc.ActivityTimestamps{Start: startTimePos, End: duration}
	}

	return
}

func getMediaInfo() (info mediaInfo, err error) {
	// funcs
	getString := func(key string) (prop string) {
		prop, err = client.GetPropertyString(key)
		if err != nil {
			log.Println("GetPropertyString error:", err)
			return ""
		}
		return
	}
	getOptionalString := func(key string) string {
		prop, getErr := client.GetPropertyString(key)
		if getErr != nil {
			log.Println("GetPropertyString error:", getErr)
			return ""
		}
		return prop
	}
	getProperty := func(key string) (prop interface{}) {
		prop, err = client.GetProperty(key)
		if err != nil {
			log.Println("GetProperty error:", err)
			return nil
		}
		return
	}

	// mpv properties
	property := getProperty("pause")
	if property != nil {
		if b, ok := property.(bool); ok {
			info.paused = b
		}
	}

	property = getProperty("time-pos")
	if property != nil {
		switch val := property.(type) {
		case float64:
			info.timePos = val
		case int:
			info.timePos = float64(val)
		case string:
			float, err := strconv.ParseFloat(val, 64)
			if err == nil {
				info.timePos = float
			}
		}
	}

	property = getProperty("idle-active")
	if property != nil {
		if b, ok := property.(bool); ok {
			info.idle = b
		}
	}

	property = getProperty("duration")
	if property != nil {
		switch val := property.(type) {
		case float64:
			info.duration = val
		case int:
			info.duration = float64(val)
		case string:
			float, err := strconv.ParseFloat(val, 64)
			if err == nil {
				info.duration = float
			}
		}
	}

	// plex
	var media plexMediaItem
	plexUAT := strings.Trim(getOptionalString("user-data/plex/user-access-token"), "\"")
	plexClientID := strings.Trim(getOptionalString("user-data/plex/client-id"), "\"")
	mediaString := getString("user-data/plex/playing-media")
	if err != nil {
		return
	}
	if mediaString == "" {
		info.idle = true
		return
	}
	err = json.Unmarshal([]byte(mediaString), &media)
	if err != nil {
		return
	}
	if media == (plexMediaItem{}) {
		info.idle = true
		return
	}
	metadataItem := media.Decision.MetadataItem

	info.contentType = media.Type
	info.mediaType = metadataItem.Type
	switch metadataItem.Type {
	case "episode":
		info.title = metadataItem.GrandparentTitle
		info.showSeason = metadataItem.ParentIndex
		info.showEpisode = metadataItem.Index
		info.showTitle = metadataItem.Title
	case "movie":
		info.title = metadataItem.Title
	default:
		info.title = metadataItem.Title
	}

	if !metadataItem.IsAdult {
		info.thumbURL = selectArtworkForDiscord(
			metadataItem.Type,
			metadataItem.Key,
			metadataItem.ParentKey,
			metadataItem.GrandparentKey,
			metadataItem.Thumb,
			metadataItem.ParentThumb,
			metadataItem.GrandparentThumb,
			media.URL,
			plexUAT,
			plexClientID,
		)
		info.legacyThumbURL = buildLegacyTokenizedArtworkURL(
			metadataItem.Type,
			metadataItem.Thumb,
			metadataItem.ParentThumb,
			metadataItem.GrandparentThumb,
			media.URL,
			plexUAT,
		)
	}

	return
}

func getActivity() (activity discordrpc.Activity, err error) {
	mediaInfo, err := getMediaInfo()
	if err != nil {
		fmt.Println("getMediaInfo Error:", err)
		return
	}

	return buildActivity(mediaInfo), nil
}

func printArtworkInspection() error {
	mediaInfo, err := getMediaInfo()
	if err != nil {
		return err
	}

	activity := buildActivity(mediaInfo)
	fmt.Printf("socket=%s\n", config.socketPath)
	fmt.Printf("idle=%t\n", mediaInfo.idle)
	fmt.Printf("paused=%t\n", mediaInfo.paused)
	fmt.Printf("media_type=%s\n", mediaInfo.mediaType)
	fmt.Printf("title=%s\n", mediaInfo.title)
	fmt.Printf("show_title=%s\n", mediaInfo.showTitle)
	if mediaInfo.thumbURL != "" {
		fmt.Println("artwork_source=public_url")
		fmt.Printf("artwork_url=%s\n", mediaInfo.thumbURL)
	} else {
		fmt.Println("artwork_source=static_fallback")
	}
	if mediaInfo.legacyThumbURL != "" {
		fmt.Printf("legacy_tokenized_artwork_url=%s\n", describeArtworkURL(mediaInfo.legacyThumbURL))
	}
	fmt.Printf("discord_large_image=%s\n", activity.LargeImageKey)
	fmt.Printf("discord_large_text=%s\n", activity.LargeImageText)
	return nil
}

func resolvedClientID(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}

	configPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "Plex", "script-opts", "discord.conf")
	data, err := os.ReadFile(configPath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "client_id=") {
				continue
			}
			clientID := strings.TrimSpace(strings.TrimPrefix(line, "client_id="))
			if clientID != "" {
				return clientID
			}
		}
	}

	return defaultDiscordCID
}

func openPresenceSync() error {
	deadline := time.Now().Add(discordOpenRetryTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if client.IsClosed() {
			return errors.New("mpv client closed before Discord RPC could open")
		}
		if err := presence.Open(); err == nil {
			log.Println("(discord-rpc): connected")
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(discordOpenRetryInterval)
	}

	if lastErr == nil {
		lastErr = errors.New("timed out opening Discord RPC")
	}
	return lastErr
}

func runTokenizedArtworkDiscordTest() error {
	mediaInfo, err := getMediaInfo()
	if err != nil {
		return err
	}
	if mediaInfo.idle {
		return errors.New("nothing is playing in Plex Desktop")
	}
	if mediaInfo.legacyThumbURL == "" {
		return errors.New("could not construct a tokenized Plex artwork URL for the current item")
	}

	activity := buildActivity(mediaInfo)
	activity.LargeImageKey = mediaInfo.legacyThumbURL

	if err := openPresenceSync(); err != nil {
		return err
	}
	if err := presence.Update(activity); err != nil {
		return err
	}

	fmt.Println("discord_test=sent")
	fmt.Printf("artwork_url=%s\n", describeArtworkURL(mediaInfo.legacyThumbURL))
	fmt.Printf("holding_presence_seconds=%d\n", int(tokenizedArtworkTestHold/time.Second))
	log.Println("Temporary tokenized artwork test sent to Discord. This bypasses the normal safety checks for validation only.")
	log.Println("Check Discord now. The process will exit after the hold interval.")
	<-time.After(tokenizedArtworkTestHold)
	return nil
}

func openClient() {
	err := client.Open(config.socketPath)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("(mpv-ipc): connected")
}

func openPresence() {
	for range time.Tick(discordOpenRetryInterval) {
		if client.IsClosed() {
			return
		}
		err := presence.Open()
		if err == nil {
			break
		}
	}
	log.Println("(discord-rpc): connected")
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Lmsgprefix)

	config = parseRuntimeConfig(os.Args)
	client = mpvipc.NewClient()
	presence = discordrpc.NewPresence(resolvedClientID(config.clientID))
}

func main() {
	defer func() {
		if !client.IsClosed() {
			err := client.Close()
			if err != nil {
				log.Fatalln(err)
			}
			log.Println("(mpv-ipc): disconnected")
		}
		if !presence.IsClosed() {
			err := presence.Close()
			if err != nil {
				log.Fatalln(err)
			}
			log.Println("(discord-rpc): disconnected")
		}
	}()

	openClient()

	if config.printArtworkOnly {
		if err := printArtworkInspection(); err != nil {
			log.Fatalln(err)
		}
		return
	}

	if config.testTokenizedArtwork {
		if err := runTokenizedArtworkDiscordTest(); err != nil {
			log.Fatalln(err)
		}
		return
	}

	go openPresence()

	for range time.Tick(time.Second) {
		if client.IsClosed() {
			return
		}
		activity, err := getActivity()
		if err != nil {
			if errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) {
				return
			}
			log.Println(err)
			continue
		}
		if !presence.IsClosed() {
			go func(a discordrpc.Activity) {
				err := presence.Update(a)
				if err != nil {
					if errors.Is(err, syscall.EPIPE) {
						err = presence.Close()
						if err != nil {
							log.Fatalln(err)
						}
						log.Println("(discord-rpc): reconnecting...")
						go openPresence()
					} else if !errors.Is(err, io.EOF) {
						log.Println(err)
					}
				}
			}(activity)
		}
	}
}
