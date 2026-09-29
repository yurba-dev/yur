package yur

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store interface {
	Has(h Hash) bool
	// The object as stored: zlib over Encode(kind, payload)
	ReadRaw(h Hash) ([]byte, error)
	// Takes a stored object from elsewhere (a pack); it is checked against its hash before it is kept
	WriteRaw(h Hash, compressed []byte) error
}

// Objects in files under dir, two hex digits of the hash for a folder, as a clone and the server keep them
type FileStore struct {
	Dir string
}

func (s FileStore) path(h Hash) string {
	hex := h.String()
	return filepath.Join(s.Dir, hex[:2], hex[2:])
}

func (s FileStore) Has(h Hash) bool {
	_, err := os.Stat(s.path(h))
	return err == nil
}

func (s FileStore) ReadRaw(h Hash) ([]byte, error) {
	return os.ReadFile(s.path(h))
}

// The bytes are checked against the hash even when the object is here already: whoever sends an object
// must show they have it. A present one is touched, so a collector sees it as just written.
func (s FileStore) WriteRaw(h Hash, compressed []byte) error {
	if err := Verify(h, compressed); err != nil {
		return err
	}
	if s.Has(h) {
		now := time.Now()
		os.Chtimes(s.path(h), now, now)
		return nil
	}
	return s.put(h, compressed)
}

func Verify(h Hash, compressed []byte) error {
	raw, err := Inflate(compressed)
	if err != nil || HashOf(raw) != h {
		return errors.New("corrupt_object")
	}
	return nil
}

// Written aside and renamed, so a reader never finds half an object
func (s FileStore) put(h Hash, compressed []byte) error {
	target := s.path(h)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp := target + ".tmp" + NewChange()[:6]
	if err := os.WriteFile(tmp, compressed, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		if s.Has(h) {
			return nil
		}
		return err
	}
	return nil
}

func Deflate(raw []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write(raw)
	w.Close()
	return b.Bytes()
}

// Objects are small next to memory, but a hostile pack could claim otherwise: the cap stops a zip bomb
const maxObject = 64 << 20

func Inflate(compressed []byte) ([]byte, error) {
	in := bytes.NewReader(compressed)
	r, err := zlib.NewReader(in)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, maxObject+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxObject {
		return nil, errors.New("object_too_large")
	}
	// Bytes after the stream would be kept and counted, but never read
	if in.Len() > 0 {
		return nil, errors.New("corrupt_object")
	}
	return raw, nil
}

// The type of an object, from its head alone: telling a chunk list from a blob needs no 64 MB inflated
func Kind(s Store, h Hash) (string, error) {
	compressed, err := s.ReadRaw(h)
	if err != nil {
		return "", errors.New("object_missing")
	}
	r, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}
	defer r.Close()
	head := make([]byte, 32)
	n, _ := io.ReadFull(r, head)
	kind, _, ok := strings.Cut(string(head[:n]), " ")
	if !ok {
		return "", errors.New("bad_object")
	}
	return kind, nil
}

func Write(s Store, kind string, payload []byte) (Hash, error) {
	raw := Encode(kind, payload)
	h := HashOf(raw)
	if s.Has(h) {
		return h, nil
	}
	return h, s.WriteRaw(h, Deflate(raw))
}

func Read(s Store, h Hash) (string, []byte, error) {
	compressed, err := s.ReadRaw(h)
	if err != nil {
		return "", nil, errors.New("object_missing")
	}
	raw, err := Inflate(compressed)
	if err != nil {
		return "", nil, err
	}
	return Decode(raw)
}

func ReadTree(s Store, h Hash) (Tree, error) {
	if h.IsZero() {
		return Tree{}, nil
	}
	kind, payload, err := Read(s, h)
	if err != nil {
		return nil, err
	}
	if kind != TypeTree {
		return nil, errors.New("not_a_tree")
	}
	return ParseTree(payload)
}

func WriteTree(s Store, t Tree) (Hash, error) {
	return Write(s, TypeTree, t.Encode())
}

// The tree with nothing in it, the base of a project's first rev
func EmptyTree(s Store) Hash {
	h, _ := WriteTree(s, Tree{})
	return h
}

func ReadRev(s Store, h Hash) (Rev, error) {
	kind, payload, err := Read(s, h)
	if err != nil {
		return Rev{}, err
	}
	if kind != TypeRev {
		return Rev{}, errors.New("not_a_rev")
	}
	return ParseRev(payload)
}

func WriteRev(s Store, r Rev) (Hash, error) {
	return Write(s, TypeRev, r.Encode())
}

// Files

// Past this a file is cut into chunks by its content, so a new version of a big file stores only what changed
const chunkAbove = 4 << 20

func WriteFile(s Store, data []byte) (Hash, error) {
	if len(data) <= chunkAbove {
		return Write(s, TypeBlob, data)
	}
	var list strings.Builder
	for _, part := range Chunks(data) {
		h, err := Write(s, TypeBlob, part)
		if err != nil {
			return h, err
		}
		list.WriteString(h.String() + "\n")
	}
	return Write(s, TypeChunks, []byte(list.String()))
}

// The most a file may come to when read whole. A chunk list may name one chunk a million times, and
// the size a tree gives is only what its writer claimed.
const MaxFile = 2 << 30

func ReadFile(s Store, h Hash) ([]byte, error) {
	return ReadFileMax(s, h, MaxFile)
}

// A file read whole, refused with file_too_large once it is past max bytes
func ReadFileMax(s Store, h Hash, max int64) ([]byte, error) {
	kind, payload, err := Read(s, h)
	if err != nil {
		return nil, err
	}
	switch kind {
	case TypeBlob:
		if int64(len(payload)) > max {
			return nil, errors.New("file_too_large")
		}
		return payload, nil
	case TypeChunks:
		parts, err := parseChunks(payload)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		for _, part := range parts {
			kind, data, err := Read(s, part)
			if err != nil {
				return nil, err
			}
			if kind != TypeBlob {
				return nil, errors.New("bad_object")
			}
			if int64(out.Len()+len(data)) > max {
				return nil, errors.New("file_too_large")
			}
			out.Write(data)
		}
		return out.Bytes(), nil
	}
	return nil, errors.New("not_a_file")
}

// The chunks a file object stands for; none when it is one blob
func FileParts(s Store, h Hash) ([]Hash, error) {
	kind, err := Kind(s, h)
	if err != nil || kind != TypeChunks {
		return nil, err
	}
	_, payload, err := Read(s, h)
	if err != nil {
		return nil, err
	}
	return parseChunks(payload)
}

func parseChunks(payload []byte) ([]Hash, error) {
	parts := []Hash{}
	for _, line := range strings.Fields(string(payload)) {
		part, err := ParseHash(line)
		if err != nil {
			return nil, errors.New("bad_object")
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// Content-defined chunks (a gear hash): an insertion moves only the cuts near it, the rest are found again
var gear = func() [256]uint64 {
	var table [256]uint64
	x := uint64(0x9E3779B97F4A7C15)
	for i := range table {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		table[i] = x
	}
	return table
}()

func Chunks(data []byte) [][]byte {
	const minChunk, maxChunk, mask = 512 << 10, 4 << 20, (1 << 20) - 1
	parts := [][]byte{}
	for len(data) > 0 {
		if len(data) <= minChunk {
			parts = append(parts, data)
			break
		}
		cut := len(data)
		if cut > maxChunk {
			cut = maxChunk
		}
		var h uint64
		for i := minChunk; i < cut; i++ {
			h = (h << 1) + gear[data[i]]
			if h&mask == 0 {
				cut = i + 1
				break
			}
		}
		parts = append(parts, data[:cut])
		data = data[cut:]
	}
	return parts
}

func NewChange() string {
	b := make([]byte, 12)
	max := big.NewInt(int64(len(changeAlphabet)))
	for i := range b {
		n, _ := rand.Int(rand.Reader, max)
		b[i] = changeAlphabet[n.Int64()]
	}
	return string(b)
}
