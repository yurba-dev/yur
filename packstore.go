package yur

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Objects as loose files while they are young, and later packed: one file per object costs a disk block
// and an inode each, a pack holds thousands in one. A pack never changes; a repack writes a new one.
//
// A pack keeps the stream format of a push (packMagic, then hash, length, zlib bytes) and has an index
// beside it: hash, offset and length of every object, sorted by hash.
type PackStore struct {
	Loose FileStore
	Dir   string

	mu    sync.RWMutex
	packs []*pack
	index map[Hash]location
	// One repack at a time: two would each drop the packs the other still reads from
	repacking sync.Mutex
}

type pack struct {
	name string
	file *os.File
}

type location struct {
	pack   *pack
	offset int64
	size   uint32
}

const indexMagic = "YURIDX01"

const indexEntry = 32 + 8 + 4

// A pack that can't be read is left out and named in the error; the store works on with the rest, as one
// bad file must not take every project down
func NewPackStore(loose string, packs string) (*PackStore, error) {
	s := &PackStore{Loose: FileStore{Dir: loose}, Dir: packs, index: map[Hash]location{}}
	if err := os.MkdirAll(packs, 0755); err != nil {
		return nil, err
	}
	// Halves of a repack that stopped midway
	tmp, _ := filepath.Glob(filepath.Join(packs, "*.tmp"))
	for _, name := range tmp {
		os.Remove(name)
	}
	names, _ := filepath.Glob(filepath.Join(packs, "*.idx"))
	sort.Strings(names)
	damaged := []string{}
	for _, idx := range names {
		if err := s.open(strings.TrimSuffix(idx, ".idx")); err != nil {
			damaged = append(damaged, err.Error())
		}
	}
	if len(damaged) > 0 {
		return s, errors.New("packs left out: " + strings.Join(damaged, "; "))
	}
	return s, nil
}

func (s *PackStore) open(base string) error {
	data, err := os.ReadFile(base + ".idx")
	if err != nil {
		return err
	}
	if len(data) < len(indexMagic) || string(data[:len(indexMagic)]) != indexMagic || (len(data)-len(indexMagic))%indexEntry != 0 {
		return errors.New("bad index " + base)
	}
	f, err := os.Open(base + ".pack")
	if err != nil {
		return err
	}
	p := &pack{name: base, file: f}
	for rest := data[len(indexMagic):]; len(rest) > 0; rest = rest[indexEntry:] {
		var h Hash
		copy(h[:], rest[:32])
		s.index[h] = location{pack: p, offset: int64(binary.BigEndian.Uint64(rest[32:40])), size: binary.BigEndian.Uint32(rest[40:44])}
	}
	s.packs = append(s.packs, p)
	return nil
}

func (s *PackStore) Has(h Hash) bool {
	return s.inPack(h) || s.Loose.Has(h)
}

func (s *PackStore) inPack(h Hash) bool {
	s.mu.RLock()
	_, ok := s.index[h]
	s.mu.RUnlock()
	return ok
}

func (s *PackStore) ReadRaw(h Hash) ([]byte, error) {
	if data, err := s.Loose.ReadRaw(h); err == nil {
		return data, nil
	}
	// Held while reading, so a repack can't close the pack under the read
	s.mu.RLock()
	defer s.mu.RUnlock()
	loc, ok := s.index[h]
	if !ok {
		return nil, os.ErrNotExist
	}
	data := make([]byte, loc.size)
	if _, err := loc.pack.file.ReadAt(data, loc.offset); err != nil {
		return nil, err
	}
	return data, nil
}

// Always loose, even for an object a pack holds: a collector may be dropping that pack's copy as dead,
// having looked before this write brought the object back
func (s *PackStore) WriteRaw(h Hash, compressed []byte) error {
	return s.Loose.WriteRaw(h, compressed)
}

// What an object weighs on the disk, stored as it is: 0 when it isn't here
func (s *PackStore) Size(h Hash) int64 {
	if info, err := os.Stat(s.Loose.path(h)); err == nil {
		return info.Size()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if loc, ok := s.index[h]; ok {
		return int64(loc.size)
	}
	return 0
}

// Every object the store holds, loose or packed, with the time a loose one was written (zero when packed)
func (s *PackStore) Each(each func(h Hash, written time.Time)) error {
	s.mu.RLock()
	packed := make([]Hash, 0, len(s.index))
	for h := range s.index {
		packed = append(packed, h)
	}
	s.mu.RUnlock()
	for _, h := range packed {
		each(h, time.Time{})
	}
	return filepath.WalkDir(s.Loose.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.Loose.Dir, p)
		h, err := ParseHash(strings.ReplaceAll(filepath.ToSlash(rel), "/", ""))
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			each(h, info.ModTime())
		}
		return nil
	})
}

// Removes a loose object; packed ones only go with a repack
func (s *PackStore) RemoveLoose(h Hash) {
	os.Remove(s.Loose.path(h))
}

// Packed objects that aren't kept, against all packed ones
func (s *PackStore) Dead(keep func(Hash) bool) (dead int, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for h := range s.index {
		total++
		if !keep(h) {
			dead++
		}
	}
	return dead, total
}

// Writes one new pack of the packed objects to keep and the loose ones given, then drops the old packs and
// those loose files. Readers go on meanwhile: the new pack is in place before anything old goes away.
func (s *PackStore) Repack(keep func(Hash) bool, loose []Hash) (packed int, err error) {
	s.repacking.Lock()
	defer s.repacking.Unlock()
	s.mu.RLock()
	old := append([]*pack(nil), s.packs...)
	kept := []Hash{}
	for h := range s.index {
		if keep(h) {
			kept = append(kept, h)
		}
	}
	s.mu.RUnlock()
	for _, h := range loose {
		if !s.inPack(h) {
			kept = append(kept, h)
		}
	}
	sortHashes(kept)

	base := filepath.Join(s.Dir, "pack-"+time.Now().UTC().Format("20060102-150405")+"-"+NewChange()[:6])
	entries, err := s.writePack(base, kept)
	if err != nil {
		for _, name := range []string{".pack.tmp", ".idx.tmp", ".pack", ".idx"} {
			os.Remove(base + name)
		}
		return 0, err
	}

	f, err := os.Open(base + ".pack")
	if err != nil {
		return 0, err
	}
	fresh := &pack{name: base, file: f}
	s.mu.Lock()
	index := make(map[Hash]location, len(entries))
	for h, loc := range entries {
		loc.pack = fresh
		index[h] = loc
	}
	s.index = index
	s.packs = []*pack{fresh}
	s.mu.Unlock()

	for _, p := range old {
		p.file.Close()
		os.Remove(p.name + ".idx")
		os.Remove(p.name + ".pack")
	}
	for _, h := range loose {
		if _, ok := entries[h]; ok {
			s.RemoveLoose(h)
		}
	}
	return len(entries), nil
}

func sortHashes(list []Hash) {
	sort.Slice(list, func(i, j int) bool { return bytes.Compare(list[i][:], list[j][:]) < 0 })
}

// The pack and its index, synced and renamed index last: an index is never there without its pack, and
// the old packs go only once both are on the disk for good
func (s *PackStore) writePack(base string, hashes []Hash) (map[Hash]location, error) {
	out, err := os.Create(base + ".pack.tmp")
	if err != nil {
		return nil, err
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 1<<20)
	offset := int64(len(packMagic))
	if _, err := w.WriteString(packMagic); err != nil {
		return nil, err
	}
	entries := map[Hash]location{}
	for _, h := range hashes {
		data, err := s.ReadRaw(h)
		if errors.Is(err, os.ErrNotExist) {
			// Gone already: nothing to carry over
			continue
		}
		if err != nil {
			return nil, err
		}
		// What goes into a pack is kept for good, so it is checked once more on the way
		if err := Verify(h, data); err != nil {
			return nil, errors.New("corrupt object " + h.String())
		}
		head := entryHead(h, len(data))
		if _, err := w.Write(head); err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
		entries[h] = location{offset: offset + int64(len(head)), size: uint32(len(data))}
		offset += int64(len(head) + len(data))
	}
	if _, err := w.Write(Zero[:]); err != nil {
		return nil, err
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	if err := out.Sync(); err != nil {
		return nil, err
	}

	sorted := make([]Hash, 0, len(entries))
	for h := range entries {
		sorted = append(sorted, h)
	}
	sortHashes(sorted)
	idx := make([]byte, 0, len(indexMagic)+len(sorted)*indexEntry)
	idx = append(idx, indexMagic...)
	for _, h := range sorted {
		loc := entries[h]
		idx = append(idx, h[:]...)
		idx = binary.BigEndian.AppendUint64(idx, uint64(loc.offset))
		idx = binary.BigEndian.AppendUint32(idx, loc.size)
	}
	if err := writeSynced(base+".idx.tmp", idx); err != nil {
		return nil, err
	}
	if err := os.Rename(base+".pack.tmp", base+".pack"); err != nil {
		return nil, err
	}
	if err := os.Rename(base+".idx.tmp", base+".idx"); err != nil {
		return nil, err
	}
	syncDir(s.Dir)
	return entries, nil
}

func writeSynced(name string, data []byte) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// The renames themselves are kept only once the folder is synced
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
