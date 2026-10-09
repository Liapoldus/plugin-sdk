package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/Liapoldus/plugin-sdk/application"
	"github.com/Liapoldus/plugin-sdk/domain/models"
)

type request struct {
	Directory json.RawMessage       `json:"directory"`
	Resolve   models.PeerResolution `json:"request"`
}

func main() {
	var input request
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		write(map[string]string{"error": "invalid_request"})
		return
	}
	directory, err := models.ParsePeerDirectory(input.Directory)
	if err != nil {
		write(map[string]string{"error": "invalid_directory"})
		return
	}
	peer, err := application.ResolvePeer(directory, input.Resolve)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrInvalidPeerDirectory):
			write(map[string]string{"error": "invalid_directory"})
		case errors.Is(err, models.ErrInvalidPeerResolution):
			write(map[string]string{"error": "invalid_resolution_request"})
		case errors.Is(err, models.ErrPeerDirectoryExpired):
			write(map[string]string{"error": "peer_directory_expired"})
		case errors.Is(err, models.ErrPeerDirectoryNotYetValid):
			write(map[string]string{"error": "peer_directory_not_yet_valid"})
		case errors.Is(err, models.ErrPeerLinkNotFound):
			write(map[string]string{"error": "peer_link_not_found"})
		case errors.Is(err, models.ErrNoEligiblePeer):
			write(map[string]string{"error": "no_eligible_peer"})
		default:
			write(map[string]string{"error": "resolution_failed"})
		}
		return
	}
	write(map[string]models.ResolvedPeer{"peer": peer})
}

func write(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		os.Exit(2)
	}
}
