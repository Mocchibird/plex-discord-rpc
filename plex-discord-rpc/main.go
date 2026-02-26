package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"strconv"
	"syscall"
	"time"

	"plex-discord-rpc/discordrpc"
	"plex-discord-rpc/mpvrpc"
)

var (
	client   *mpvrpc.Client
	presence *discordrpc.Presence
	currTime = time.Now().Local().UnixMilli()
)

const (
	largeImageKey  = "logo"
	largeImageText = "Plex"

	imagePlay  = "play"
	imagePause = "pause"
	imageIdle  = "stop"

	pollInterval  = time.Second
	retryInterval = 500 * time.Millisecond
)

type plexMediaItem struct {
	URL      string `json:"url"`
	Type     string `json:"type"`
	Decision struct {
		MetadataItem struct {
			Title            string `json:"title"`
			ServerEntityID   string `json:"serverEntityID"`
			Thumb            string `json:"thumb"`
			Index            int    `json:"index"`
			Type             string `json:"type"`
			IsAdult          bool   `json:"isAdult"`
			GrandparentTitle string `json:"grandparentTitle"`
			GrandparentThumb string `json:"grandparentThumb"`
			ParentThumb      string `json:"parentThumb"`
			ParentIndex      int    `json:"parentIndex"`
		} `json:"metadataItem"`
	} `json:"decision"`
}

type mediaInfo struct {
	title       string
	contentType string
	mediaType   string
	showTitle   string
	showSeason  int
	showEpisode int
	thumbURL    string
	paused      bool
	idle        bool
	timePos     float64
	duration    float64
}

type plexResource struct {
	Name             string `json:"name"`
	ClientIdentifier string `json:"clientIdentifier"`
	AccessToken      string `json:"accessToken"`
	Provides         string `json:"provides"`
	Connections      []struct {
		URI   string `json:"uri"`
		Local bool   `json:"local"`
		Relay bool   `json:"relay"`
	} `json:"connections"`
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Lmsgprefix)

	client = mpvrpc.NewClient()
	presence = discordrpc.NewPresence(os.Args[2])
}

func getMediaInfo() (info mediaInfo, err error) {
	getString := func(key string) (prop string) {
		prop, err = client.GetPropertyString(key)
		if err != nil {
			log.Println("GetPropertyString error:", err)
			return ""
		}
		return
	}
	getProperty := func(key string) (prop interface{}) {
		prop, err = client.GetProperty(key)
		if err != nil {
			log.Println("GetPropertyString error:", err)
			return ""
		}
		return
	}

	if v := getProperty("pause"); v != nil {
		if b, ok := v.(bool); ok {
			info.paused = b
		}
	}
	
	if v := getProperty("time-pos"); v != nil {
		switch val := v.(type) {
		case float64:
			info.timePos = val
		case int:
			info.timePos = float64(val)
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				info.timePos = f
			}
		}
	}
	
	if v := getProperty("idle-active"); v != nil {
		if b, ok := v.(bool); ok {
			info.idle = b
			if b {
				log.Println("idle")
				return
			}
		}
	}
	
	if v := getProperty("duration"); v != nil {
		switch val := v.(type) {
		case float64:
			info.duration = val
		case int:
			info.duration = float64(val)
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				info.duration = f
			}
		}
	}

	var media plexMediaItem
	plexUAT := strings.Trim(getString("user-data/plex/user-access-token"), "\"")
	plexClientID := strings.Trim(getString("user-data/plex/client-id"), "\"")
	mediaString := getString("user-data/plex/playing-media")
	if mediaString == "" {
		info.idle = true
		return
	}
	if e := json.Unmarshal([]byte(mediaString), &media); e != nil {
		info.idle = true
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

	if media.URL != "" && plexUAT != "" && plexClientID != "" && !metadataItem.IsAdult {
		parsed, e := url.Parse(media.URL)
		if e != nil {
			return
		}
		var baseURL = parsed.Scheme + "://" + parsed.Host

		req, _ := http.NewRequest("GET", "https://clients.plex.tv/api/v2/resources?includeHttps=1", nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Product", largeImageText)
		req.Header.Set("X-Plex-Token", plexUAT)
		req.Header.Set("X-Plex-Client-Identifier", plexClientID)

		res, e := http.DefaultClient.Do(req)
		if e != nil {
			return
		}
		defer res.Body.Close()

		var response []plexResource
		e = json.NewDecoder(res.Body).Decode(&response)
		if e != nil {
			return
		}

		var token = ""
		for i := range response {
			if strings.Contains(response[i].Provides, "server") {
				if strings.Contains(metadataItem.ServerEntityID, response[i].ClientIdentifier) {
					token = response[i].AccessToken
					break
				}

				for _, c := range response[i].Connections {
					u, _ := url.Parse(c.URI)
					if u.Host == parsed.Host {
						token = response[i].AccessToken
						break
					}
				}

				if token != "" {
					break
				}
			}
		}

		if token != "" {
			switch metadataItem.Type {
			case "episode":
				if metadataItem.ParentThumb != "" {
					info.thumbURL = baseURL + metadataItem.ParentThumb + "?X-Plex-Token=" + token
				} else if metadataItem.GrandparentThumb != "" {
					info.thumbURL = baseURL + metadataItem.GrandparentThumb + "?X-Plex-Token=" + token
				}
			case "movie":
				if metadataItem.Thumb != "" {
					info.thumbURL = baseURL + metadataItem.Thumb + "?X-Plex-Token=" + token
				}
			default:
				if metadataItem.Thumb != "" {
					info.thumbURL = baseURL + metadataItem.Thumb + "?X-Plex-Token=" + token
				}
			}
		}
	}

	return
}

func getActivity() (activity discordrpc.Activity, err error) {
	mediaInfo, err := getMediaInfo()
	if err != nil {
		fmt.Println("Error:", err)
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
	activity.Type = 3
	if mediaInfo.mediaType == "episode" && mediaInfo.showTitle != "" {
		activity.State = fmt.Sprintf("S%02dE%02d - %s", mediaInfo.showSeason, mediaInfo.showEpisode, mediaInfo.showTitle)
	} else if mediaInfo.mediaType == "movie" && mediaInfo.title != "" {
		activity.State = ""
	} else {
		activity.State = ""
	}

	// Small Image
	if mediaInfo.idle {
		activity.SmallImageKey = imageIdle
		activity.SmallImageText = "Nothing is Playing"
	} else if mediaInfo.paused {
		activity.SmallImageKey = imagePause
		activity.SmallImageText = "Paused"
	} else {
		activity.SmallImageKey = imagePlay
		activity.SmallImageText = "Playing"
	}

	// Timestamps
	if mediaInfo.duration > 0 && mediaInfo.timePos > 0 {
		startTimePos := currTime - (int64(mediaInfo.timePos) * 1000)
		duration := startTimePos + (int64(mediaInfo.duration) * 1000)

		if !mediaInfo.paused && !mediaInfo.idle {
			activity.Timestamps = &discordrpc.ActivityTimestamps{Start: startTimePos, End: duration}
			currTime = time.Now().Local().UnixMilli()
		}
	}
	return
}

func openClient() {
	if err := client.Open(os.Args[1]); err != nil {
		log.Fatalln(err)
	}
	log.Println("(mpv-ipc): connected")
}

func openPresence() {
	for range time.Tick(500 * time.Millisecond) {
		if client.IsClosed() {
			return
		}
		if err := presence.Open(); err == nil {
			break
		}
	}
	log.Println("(discord-rpc): connected")
}

func main() {
	defer func() {
		if !client.IsClosed() {
			if err := client.Close(); err != nil {
				log.Fatalln(err)
			}
			log.Println("(mpv-ipc): disconnected")
		}
		if !presence.IsClosed() {
			if err := presence.Close(); err != nil {
				log.Fatalln(err)
			}
			log.Println("(discord-rpc): disconnected")
		}
	}()

	openClient()
	go openPresence()

	for range time.Tick(time.Second) {
		activity, err := getActivity()
		if err != nil {
			if errors.Is(err, syscall.EPIPE) {
				break
			} else if !errors.Is(err, io.EOF) {
				client.Close()
			}
		}
		if !presence.IsClosed() {
			go func() {
				if err = presence.Update(activity); err != nil {
					if errors.Is(err, syscall.EPIPE) {
						if err = presence.Close(); err != nil {
							log.Fatalln(err)
						}
						log.Println("(discord-rpc): reconnecting...")
						go openPresence()
					} else if !errors.Is(err, io.EOF) {
						log.Println(err)
					}
				}
			}()
		}
	}
}
