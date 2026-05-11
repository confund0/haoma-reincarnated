package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
	"haoma-frontend/internal/vault"
)

const opkPrefix = "signal:opk:"

func main() {
	dataDir := flag.String("data-dir", "", "haoma frontend data-dir (contains meta.json + badger/)")
	target := flag.Int("target", 29, "drain pool down to this many keys (deletes the lowest ids)")
	vaultPath := flag.String("vault", "", "path to vault.enc; when set, HAOMA_VAULT_PASSPHRASE is used to extract the frontend store pass")
	flag.Parse()

	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "--data-dir required")
		os.Exit(2)
	}
	pw, err := resolvePassphrase(*vaultPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	st, err := store.Unlock(*dataDir, pw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unlock: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = st.Lock() }()

	var ids []uint32
	if err := st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(opkPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().Key()
			if len(k) == len(opkPrefix)+4 {
				ids = append(ids, binary.BigEndian.Uint32(k[len(opkPrefix):]))
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	fmt.Printf("OPK pool before: %d keys (range %v..%v)\n", len(ids), firstOr(ids, 0), lastOr(ids, 0))

	if *target < 0 {
		fmt.Fprintln(os.Stderr, "--target must be >= 0")
		os.Exit(2)
	}
	if len(ids) <= *target {
		fmt.Println("nothing to do; pool already <= target")
		return
	}

	toDelete := ids[:len(ids)-*target]
	if err := st.Update(func(txn *badger.Txn) error {
		for _, id := range toDelete {
			k := make([]byte, 0, len(opkPrefix)+4)
			k = append(k, opkPrefix...)
			var idBE [4]byte
			binary.BigEndian.PutUint32(idBE[:], id)
			k = append(k, idBE[:]...)
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "delete: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("deleted %d keys (ids %d..%d)\n", len(toDelete), toDelete[0], toDelete[len(toDelete)-1])
	fmt.Printf("OPK pool after:  %d keys (range %d..%d)\n", *target, ids[len(ids)-*target], ids[len(ids)-1])
}

func resolvePassphrase(vaultPath string) (string, error) {
	if vaultPath != "" {
		master := os.Getenv("HAOMA_VAULT_PASSPHRASE")
		if master == "" {
			return "", fmt.Errorf("HAOMA_VAULT_PASSPHRASE empty (required when --vault is set)")
		}
		payload, _, err := vault.Open(vaultPath, master)
		if err != nil {
			return "", fmt.Errorf("vault open: %w", err)
		}
		if payload.FrontendStorePassphrase == "" {
			return "", fmt.Errorf("vault payload has no frontend_store_passphrase")
		}
		return payload.FrontendStorePassphrase, nil
	}
	pw := os.Getenv("HAOMA_FRONTEND_PASSPHRASE")
	if pw == "" {
		return "", fmt.Errorf("HAOMA_FRONTEND_PASSPHRASE empty (or pass --vault to extract from vault.enc)")
	}
	return pw, nil
}

func firstOr(ids []uint32, fallback uint32) uint32 {
	if len(ids) == 0 {
		return fallback
	}
	return ids[0]
}

func lastOr(ids []uint32, fallback uint32) uint32 {
	if len(ids) == 0 {
		return fallback
	}
	return ids[len(ids)-1]
}
