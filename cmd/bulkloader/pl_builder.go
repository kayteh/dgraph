package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/dgraph-io/badger"
	"github.com/dgraph-io/dgraph/protos"
	"github.com/dgraph-io/dgraph/x"
)

func newPlBuilder(tmpDir string) plBuilder {
	badgerDir, err := ioutil.TempDir(tmpDir, "dgraph_bulkloader")
	x.Check(err)
	opt := badger.DefaultOptions
	opt.Dir = badgerDir
	opt.ValueDir = badgerDir
	kv, err := badger.NewKV(&opt)
	x.Check(err)
	return plBuilder{kv, badgerDir}
}

type plBuilder struct {
	kv        *badger.KV
	badgerDir string
}

func (b *plBuilder) cleanUp() {
	x.Check(os.RemoveAll(b.badgerDir))
}

func (b *plBuilder) addPosting(postingListKey []byte, posting *protos.Posting) {

	var uidBuf [8]byte
	binary.BigEndian.PutUint64(uidBuf[:], posting.Uid)

	key := postingListKey
	key = append(key, uidBuf[:]...)

	var meta byte
	var val []byte
	switch posting.PostingType {
	case protos.Posting_REF:
		// val is left nil. When we read back the key/value, the UID is
		// recovered from the key.
		meta = 0x01 // Indicates posting UID rather than protos.Posting
	case protos.Posting_VALUE:
		var err error
		val, err = posting.Marshal()
		x.Check(err)
	case protos.Posting_VALUE_LANG:
		x.AssertTrue(false) // TODO
	default:
		x.AssertTrue(false)
	}

	x.Check(b.kv.Set(key, val, meta))
}

func (b *plBuilder) buildPostingLists(target *badger.KV) {

	// TODO: We should really be opening the KV here as well. Better to store
	// the config in plBuilder rather than the KV itself.
	defer func() {
		x.Check(b.kv.Close())
	}()

	pl := &protos.PostingList{}
	uids := []uint64{}
	iter := b.kv.NewIterator(badger.DefaultIteratorOptions)
	iter.Seek(nil)
	if !iter.Valid() {
		// There were no posting lists to build.
		return
	}
	k := extractPLKey(iter.Item().Key())
	for iter.Valid() {

		// Add to PL
		// TODO: Add a check here to make sure all postings have the same user meta.
		if iter.Item().UserMeta() == 0x01 {
			uids = append(uids, extractUID(iter.Item().Key()))
		} else {
			p := new(protos.Posting)
			err := p.Unmarshal(iter.Item().Value())
			x.Check(err)
			uids = append(uids, p.Uid)
			pl.Postings = append(pl.Postings, p)
		}

		// Determine if we're at the end of a single posting list.
		finalise := false
		iter.Next()
		var newK []byte
		if iter.Valid() {
			newK = extractPLKey(iter.Item().Key())
			if bytes.Compare(newK, k) != 0 {
				finalise = true
			}
		} else {
			finalise = true
		}

		// Write posting list out to target.
		if finalise {

			simplePostingList := len(uids) != len(pl.Postings)

			fmt.Print("KEY:\n" + hex.Dump(k))
			fmt.Println("POSTINGS:")
			if simplePostingList {
				for _, p := range uids {
					fmt.Println(p)
				}
			} else {
				for _, p := range pl.Postings {
					fmt.Printf("%#v\n", p)
				}
			}
			fmt.Println("END POSTINGS\n")

			if simplePostingList {
				x.Check(target.Set(k, bitPackUids(uids), 0x01))
			} else {
				pl.Uids = bitPackUids(uids)
				plBuf, err := pl.Marshal()
				x.Check(err)
				x.Check(target.Set(k, plBuf, 0x00))
			}

			// Reset for next posting list.
			pl.Postings = nil
			pl.Uids = nil
			uids = nil
		}
		k = newK
	}
}

func extractPLKey(kvKey []byte) []byte {
	// Copy value since it's only valid until the iterator is next advanced.
	x.AssertTrue(len(kvKey) > 8)
	k := make([]byte, len(kvKey)-8)
	copy(k, kvKey)
	return k
}

func extractUID(kvKey []byte) uint64 {
	return binary.BigEndian.Uint64(kvKey[len(kvKey)-8:])
}
