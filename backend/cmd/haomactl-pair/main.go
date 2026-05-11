package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"haoma/internal/peers"
	"haoma/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "emit":
		emitCmd(os.Args[2:])
	case "import":
		importCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "haomactl-pair: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `haomactl-pair: paste-in peer pairing (dev only).

subcommands:
  emit    emit a pairing invite JSON to stdout
  import  import a pairing invite JSON from stdin into the store

emit flags:
  --address=<onion>  v3 service ID, no ".onion". Repeatable.
  --secret=<hex>     reuse a specific 32-byte outbound secret (rare;
                     mostly for tests / reproducible runs).

import flags:
  --store=<dir>           path to the encrypted store
  --passphrase=<pw>       store passphrase
  --my-outbound=<hex>     OUR outbound key for this channel (the
                          'secret' value from our own emit).`)
}

func emitCmd(args []string) {
	fs := flag.NewFlagSet("emit", flag.ExitOnError)
	var addresses multiFlag
	fs.Var(&addresses, "address", "v3 onion service ID. Repeatable.")
	secretHex := fs.String("secret", "", "hex-encoded 32-byte shared secret; generated if empty")
	if err := fs.Parse(args); err != nil {
		die("parse flags: %v", err)
	}
	if len(addresses) == 0 {
		die("emit: --address is required (at least once)")
	}

	var secret []byte
	if *secretHex != "" {
		b, err := hex.DecodeString(*secretHex)
		if err != nil {
			die("emit: --secret is not valid hex: %v", err)
		}
		if len(b) != 32 {
			die("emit: --secret decodes to %d bytes, want 32", len(b))
		}
		secret = b
	}

	inv, err := peers.NewInvite([]string(addresses), secret)
	if err != nil {
		die("emit: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inv); err != nil {
		die("emit: encode: %v", err)
	}
}

func importCmd(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	storeDir := fs.String("store", "", "path to the encrypted store")
	passphrase := fs.String("passphrase", "", "store passphrase")
	myOutbound := fs.String("my-outbound", "", "hex-encoded 32-byte OUR outbound key for this channel")
	if err := fs.Parse(args); err != nil {
		die("parse flags: %v", err)
	}
	if *storeDir == "" || *passphrase == "" {
		die("import: --store and --passphrase are required")
	}
	if *myOutbound == "" {
		die("import: --my-outbound is required (the 'secret' value from your own emit)")
	}
	myOutBytes, err := hex.DecodeString(*myOutbound)
	if err != nil {
		die("import: --my-outbound is not valid hex: %v", err)
	}
	if len(myOutBytes) != 32 {
		die("import: --my-outbound decodes to %d bytes, want 32", len(myOutBytes))
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		die("import: read stdin: %v", err)
	}
	var inv peers.Invite
	if err := json.Unmarshal(raw, &inv); err != nil {
		die("import: parse invite: %v", err)
	}
	be := peers.BackendInvite{
		PeerID:         inv.PeerID,
		Addresses:      inv.Addresses,
		InboundSecret:  inv.Secret,
		OutboundSecret: hex.EncodeToString(myOutBytes),
	}

	s, err := store.Unlock(*storeDir, *passphrase)
	if err != nil {
		die("import: unlock store: %v", err)
	}
	defer s.Lock()

	r := peers.NewRegistry(s)
	retired, err := r.Import(&be)
	if err != nil {
		die("import: %v", err)
	}
	fmt.Fprintf(os.Stderr, "added peer %s (%d address(es))\n", inv.PeerID, len(inv.Addresses))
	for _, rid := range retired {
		fmt.Fprintf(os.Stderr, "retired prior peer %s (address collision)\n", rid)
	}
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "haomactl-pair: "+format+"\n", args...)
	os.Exit(1)
}

type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }
