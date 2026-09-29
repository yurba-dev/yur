package yur

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// A pack is objects one after another as they are stored: hash, length, zlib bytes. It ends with a zero hash.
const packMagic = "YURPACK1"

type PackWriter struct {
	w *bufio.Writer
}

func NewPackWriter(w io.Writer) (*PackWriter, error) {
	p := &PackWriter{w: bufio.NewWriterSize(w, 1<<16)}
	_, err := p.w.WriteString(packMagic)
	return p, err
}

func (p *PackWriter) Add(h Hash, compressed []byte) error {
	if _, err := p.w.Write(entryHead(h, len(compressed))); err != nil {
		return err
	}
	_, err := p.w.Write(compressed)
	return err
}

// What comes before an object's bytes in a pack: its hash and length
func entryHead(h Hash, size int) []byte {
	head := make([]byte, 0, len(h)+binary.MaxVarintLen64)
	head = append(head, h[:]...)
	return binary.AppendUvarint(head, uint64(size))
}

func (p *PackWriter) Close() error {
	if _, err := p.w.Write(Zero[:]); err != nil {
		return err
	}
	return p.w.Flush()
}

func WritePack(w io.Writer, s Store, hashes []Hash) error {
	p, err := NewPackWriter(w)
	if err != nil {
		return err
	}
	for _, h := range hashes {
		raw, err := s.ReadRaw(h)
		if err != nil {
			return errors.New("object_missing")
		}
		if err := p.Add(h, raw); err != nil {
			return err
		}
	}
	return p.Close()
}

// Reads a pack into the store; each object is checked against its hash on the way. Gives what came.
func ReadPack(r io.Reader, s Store, each func(h Hash)) (int, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	magic := make([]byte, len(packMagic))
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != packMagic {
		return 0, errors.New("bad_pack")
	}
	count := 0
	for {
		var h Hash
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return count, errors.New("bad_pack")
		}
		if h.IsZero() {
			return count, nil
		}
		size, err := binary.ReadUvarint(br)
		if err != nil || size > maxObject {
			return count, errors.New("bad_pack")
		}
		compressed := make([]byte, size)
		if _, err := io.ReadFull(br, compressed); err != nil {
			return count, errors.New("bad_pack")
		}
		if err := s.WriteRaw(h, compressed); err != nil {
			return count, err
		}
		if each != nil {
			each(h)
		}
		count++
	}
}

// What the other side needs to have the revs in want, given it has those in have and all below them.
// Without blobs only revs and trees go, the files are asked for when they are opened (a light clone).
func Needed(s Store, want []Hash, have []Hash, blobs bool) ([]Hash, error) {
	order, revs, err := Only(s, want, have, 0)
	if err != nil {
		return nil, err
	}

	seen := map[Hash]bool{}
	out := []Hash{}
	add := func(h Hash) bool {
		if seen[h] {
			return false
		}
		seen[h] = true
		out = append(out, h)
		return true
	}
	var walk func(now, before Hash) error
	walk = func(now, before Hash) error {
		if now == before || !add(now) {
			return nil
		}
		tree, err := ReadTree(s, now)
		if err != nil {
			return err
		}
		old, err := ReadTree(s, before)
		if err != nil {
			return err
		}
		oldNames := old.byName()
		for _, e := range tree {
			prev, ok := oldNames[e.Name]
			if ok && prev.Hash == e.Hash {
				continue
			}
			if e.Kind == KindDir {
				under := Zero
				if ok && prev.Kind == KindDir {
					under = prev.Hash
				}
				if err := walk(e.Hash, under); err != nil {
					return err
				}
				continue
			}
			if !blobs {
				continue
			}
			if add(e.Hash) {
				parts, err := FileParts(s, e.Hash)
				if err != nil {
					return err
				}
				for _, part := range parts {
					add(part)
				}
			}
		}
		return nil
	}
	// Parents first, so whoever receives them never holds a rev whose parent is still to come
	for _, h := range Topo(order, revs) {
		r := revs[h]
		before := Zero
		if len(r.Parents) > 0 {
			if p, err := ReadRev(s, r.Parents[0]); err == nil {
				before = p.Tree
			}
		}
		if err := walk(r.Tree, before); err != nil {
			return nil, err
		}
		add(h)
	}
	return out, nil
}
