package crawler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

type ipAPIResponse struct {
	Status    string  `json:"status"`
	City      string  `json:"city"`
	Region    string  `json:"regionName"`
	Country   string  `json:"country"`
	Continent string  `json:"continent"`
	ISP       string  `json:"isp"`
	Org       string  `json:"org"`
	AS        string  `json:"as"`
	ASName    string  `json:"asname"`
	Lat       float32 `json:"lat"`
	Lon       float32 `json:"lon"`
	Query     string  `json:"query"`
}

const (
	ipapiurl = "http://ip-api.com/batch?fields=status,city,regionName,country,continent,isp,org,as,asname,lat,lon,query"
)

func (m *Manager) geoIP(ctx context.Context) {

	client := http.Client{
		Timeout: time.Second * 3,
	}

	var toFind []string

	m.mtx.RLock()
	for ip, node := range m.nodes {
		if node.GeoData == nil {
			toFind = append(toFind, ip)
		}
	}
	m.mtx.RUnlock()

	if len(toFind) == 0 {
		return
	}

	log.Printf("Missing geo data for %d nodes", len(toFind))

	// Split IPs into groups of 100, the max supported by ip-api
	var divided [][]string
	chunkSize := 100
	for i := 0; i < len(toFind); i += chunkSize {
		end := i + chunkSize
		if end > len(toFind) {
			end = len(toFind)
		}
		divided = append(divided, toFind[i:end])
	}

	for _, div := range divided {

		reqData, err := json.Marshal(div)
		if err != nil {
			log.Printf("json.Marshal error: %v", err)
			return
		}

		req, err := http.NewRequest(http.MethodPost, ipapiurl, bytes.NewBuffer(reqData))
		if err != nil {
			log.Printf("http.NewRequest error: %v", err)
			return
		}

		res, getErr := client.Do(req)
		if getErr != nil {
			log.Printf("client.Do error: %v", getErr)
			return
		}

		if res.Body != nil {
			defer res.Body.Close()
		}

		body, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			log.Printf("io.ReadAll: %v", readErr)
			return
		}

		var g []ipAPIResponse
		jsonErr := json.Unmarshal(body, &g)
		if jsonErr != nil {
			if res.Status == "429 Too Many Requests" {
				log.Printf("Unexpectedly hit ip-api rate limit, waiting %d seconds", 60)
				select {
				case <-time.After(time.Second * 60):
					return
				case <-ctx.Done():
					return
				}
			}

			log.Printf("json.Unmarshal error: %v", jsonErr)
			log.Printf("resp status: %v", res.Status)
			log.Printf("resp body: %v", string(body))
			return
		}

		log.Printf("Got %d geo locations", len(g))

		for _, geo := range g {
			if geo.Status != "success" {
				log.Printf("Skipping non-success geo, status: %s, query: %s", geo.Status, geo.Query)
				continue
			}

			m.mtx.Lock()
			node, ok := m.nodes[geo.Query]
			if !ok {
				log.Printf("Received geo for non-existing node %s", geo.Query)
				continue
			}
			node.GeoData = &GeoData{
				City:      geo.City,
				Region:    geo.Region,
				Country:   geo.Country,
				Continent: geo.Continent,
				ISP:       geo.ISP,
				Org:       geo.Org,
				AS:        geo.AS,
				ASName:    geo.ASName,
				Lat:       geo.Lat,
				Lon:       geo.Lon,
			}
			m.mtx.Unlock()
		}

		// Check response headers for rate limits
		remainingReqs, err := strconv.Atoi(res.Header.Get("X-Rl"))
		if err != nil {
			log.Printf("ip-api response missing X-Rl header")
			return
		}

		if remainingReqs == 0 {
			timeToWait, err := strconv.Atoi(res.Header.Get("X-Ttl"))
			if err != nil {
				log.Printf("ip-api response missing X-Ttl header")
				return
			}

			log.Printf("Hit ip-api rate limit. Waiting %d seconds", timeToWait+5)
			select {
			case <-time.After(time.Second * time.Duration(timeToWait+5)):
			case <-ctx.Done():
				return
			}
		}

	}

}
