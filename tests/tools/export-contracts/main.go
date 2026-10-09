// Tool export-contracts derives public JSON constants from SDK Go definitions.
// JSON schemas derive from the same code-owned declarations as runtime exports.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	sdk "github.com/Liapoldus/plugin-sdk/infrastructure"
)

func main() {
	root := flag.String("root", ".", "SDK repository root")
	check := flag.Bool("check", false, "check generated constants without writing")
	flag.Parse()
	definitions := map[string]any{}
	h, err := sdk.LoadHTTPContract()
	must(err)
	definitions["v1/http-contract.json"] = h
	r, err := sdk.LoadReplicaLifecycleContract()
	must(err)
	definitions["v2/replica-lifecycle.json"] = r
	p, err := sdk.LoadPeerDirectoryPollContract()
	must(err)
	definitions["v2/peer-directory-poll.json"] = p
	l, err := sdk.LoadLoopbackPlaintextProfile()
	must(err)
	definitions["v2/loopback-plaintext-profile.json"] = l
	definitions["v2/peer-directory.schema.json"] = json.RawMessage(sdk.PeerDirectorySchema())
	definitions["v2/replica-lifecycle.schema.json"] = json.RawMessage(sdk.ReplicaLifecycleSchema())
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := json.MarshalIndent(definitions[name], "", "  ")
		must(err)
		data = append(data, '\n')
		path := filepath.Join(*root, "infrastructure/assets/plugin-sdk", filepath.FromSlash(name))
		existing, err := os.ReadFile(path) //nolint:gosec // path is constrained to the repository contract output directory.
		must(err)
		if bytes.Equal(existing, data) {
			continue
		}
		if *check {
			must(fmt.Errorf("generated contract is stale: %s (run make contracts)", name))
		}
		must(os.WriteFile(path, data, 0644)) //nolint:gosec // generated public contracts are intentionally world-readable.
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
