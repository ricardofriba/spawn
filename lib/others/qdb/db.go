// Package qdb -
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
/*
Qdb is a fast persistent storage database.

The records are binary blobs that can have a variable length, up to 4GB.

The key must be a unique 64-bit value, most likely a hash of the actual key.

They data is stored on a disk, in a folder specified during the call to NewDB().
There are can be three possible files in that folder
 * qdb.0, qdb.1 - these files store a compact version of the entire database
 * qdb.log - this one stores the changes since the most recent qdb.0 or qdb.1

*/
package qdb

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sync"
)

// KeyType -
type KeyType uint64

var (
	// ExtraMemoryConsumed -
	ExtraMemoryConsumed int64 // if we are using the glibc memory manager
	// ExtraMemoryAllocCnt -
	ExtraMemoryAllocCnt int64 // if we are using the glibc memory manager
)

const (
	// KeySize -
	KeySize = 8
	// NoBrowse -
	NoBrowse = 0x00000001
	// NoCache -
	NoCache = 0x00000002
	// BrAbort -
	BrAbort = 0x00000004
	// YesCache -
	YesCache = 0x00000008
	// YesBrowse -
	YesBrowse = 0x00000010
	// DefaultDefragPercentVal -
	DefaultDefragPercentVal = 50
	// DefaultForcedDefragPerc -
	DefaultForcedDefragPerc = 300
	// DefaultMaxPending -
	DefaultMaxPending = 2500
	// DefaultMaxPendingNoSync -
	DefaultMaxPendingNoSync = 10000
)

// DB -
type DB struct {
	// folder with the db files
	Dir string

	LogFile         *os.File
	LastValidLogPos int64
	DataSeq         uint32

	// access mutex:
	Mutex sync.Mutex

	//index:
	Idx *Index

	NoSyncMode     bool
	PendingRecords map[KeyType]bool

	DatFiles map[uint32]*os.File

	O ExtraOpts

	VolatileMode bool // this will only store database on disk when you close it
}

type oneIdx struct {
	data data_ptr_t

	DataSeq uint32 // data file index
	datpos  uint32 // position of the record in the data file
	datlen  uint32 // length of the record in the data file

	flags uint32
}

// NewDBOpts -
type NewDBOpts struct {
	Dir          string
	Records      uint
	WalkFunction WalkFunction
	LoadData     bool
	Volatile     bool
	*ExtraOpts
}

// ExtraOpts -
type ExtraOpts struct {
	DefragPercentVal uint32 // Defrag() will not be done if we waste less disk space
	ForcedDefragPerc uint32 // forced defrag when extra disk usage goes above this
	MaxPending       uint32
	MaxPendingNoSync uint32
}

// WalkFunction -
type WalkFunction func(key KeyType, val []byte) uint32

func (idx oneIdx) String() string {
	if idx.data == nil {
		return fmt.Sprintf("Nodata:%d:%d:%d", idx.DataSeq, idx.datpos, idx.datlen)
	}
	return fmt.Sprintf("YesData:%d:%d:%d", idx.DataSeq, idx.datpos, idx.datlen)
}

// NewDBExt - Creates or opens a new database in the specified folder.
func NewDBExt(_db **DB, opts *NewDBOpts) (e error) {
	cnt("NewDB")
	db := new(DB)
	*_db = db
	dir := opts.Dir
	if len(dir) > 0 && dir[len(dir)-1] != '\\' && dir[len(dir)-1] != '/' {
		dir += string(os.PathSeparator)
	}

	db.VolatileMode = opts.Volatile

	if opts.ExtraOpts == nil {
		db.O.DefragPercentVal = DefaultDefragPercentVal
		db.O.ForcedDefragPerc = DefaultForcedDefragPerc
		db.O.MaxPending = DefaultMaxPending
		db.O.MaxPendingNoSync = DefaultMaxPendingNoSync
	} else {
		db.O = *opts.ExtraOpts
	}

	os.MkdirAll(dir, 0770)
	db.Dir = dir
	db.DatFiles = make(map[uint32]*os.File)
	db.PendingRecords = make(map[KeyType]bool, db.O.MaxPending)

	db.Idx = NewDBidx(db, opts.Records)
	if opts.LoadData {
		db.Idx.load(opts.WalkFunction)
	}
	db.DataSeq = db.Idx.MaxDatfileSequence + 1
	return
}

// NewDB -
func NewDB(dir string, load bool) (*DB, error) {
	var db *DB
	e := NewDBExt(&db, &NewDBOpts{Dir: dir, LoadData: load})
	return db, e
}

// Count - Returns number of records in the DB
func (db *DB) Count() (l int) {
	db.Mutex.Lock()
	l = db.Idx.size()
	db.Mutex.Unlock()
	return
}

// Browse - Browses through all the DB records calling the walk function for each record.
// If the walk function returns false, it aborts the browsing and returns.
func (db *DB) Browse(walk WalkFunction) {
	db.Mutex.Lock()
	db.Idx.browse(func(k KeyType, v *oneIdx) bool {
		if (v.flags & NoBrowse) != 0 {
			return true
		}
		db.loadrec(v)
		res := walk(k, v.Slice())
		v.applyBrowsingFlags(res)
		v.freerec()
		return (res & BrAbort) == 0
	})
	//println("br", db.Dir, "done")
	db.Mutex.Unlock()
}

// BrowseAll - works almost like normal browse except that it also returns non-browsable records
func (db *DB) BrowseAll(walk WalkFunction) {
	db.Mutex.Lock()
	db.Idx.browse(func(k KeyType, v *oneIdx) bool {
		db.loadrec(v)
		res := walk(k, v.Slice())
		v.applyBrowsingFlags(res)
		v.freerec()
		return (res & BrAbort) == 0
	})
	//println("br", db.Dir, "done")
	db.Mutex.Unlock()
}

// Get -
func (db *DB) Get(key KeyType) (value []byte) {
	db.Mutex.Lock()
	idx := db.Idx.get(key)
	if idx != nil {
		db.loadrec(idx)
		idx.applyBrowsingFlags(YesCache) // we are giving out the pointer, so keep it in cache
		value = idx.Slice()
	}
	//fmt.Printf("get %016x -> %s\n", key, hex.EncodeToString(value))
	db.Mutex.Unlock()
	return
}

// GetNoMutex - Use this one inside Browse
func (db *DB) GetNoMutex(key KeyType) (value []byte) {
	idx := db.Idx.get(key)
	if idx != nil {
		db.loadrec(idx)
		value = idx.Slice()
	}
	//fmt.Printf("get %016x -> %s\n", key, hex.EncodeToString(value))
	return
}

// Put - Adds or updates record with a given key.
func (db *DB) Put(key KeyType, value []byte) {
	db.Mutex.Lock()
	db.Idx.memput(key, newIdx(value, 0))
	if db.VolatileMode {
		db.NoSyncMode = true
		db.Mutex.Unlock()
		return
	}
	db.PendingRecords[key] = true
	if db.syncneeded() {
		go func() {
			db.sync()
			db.Mutex.Unlock()
		}()
	} else {
		db.Mutex.Unlock()
	}
}

// PutExt - Adds or updates record with a given key.
func (db *DB) PutExt(key KeyType, value []byte, flags uint32) {
	db.Mutex.Lock()
	//fmt.Printf("put %016x %s\n", key, hex.EncodeToString(value))
	db.Idx.memput(key, newIdx(value, flags))
	if db.VolatileMode {
		db.NoSyncMode = true
		db.Mutex.Unlock()
		return
	}
	db.PendingRecords[key] = true
	if db.syncneeded() {
		go func() {
			db.sync()
			db.Mutex.Unlock()
		}()
	} else {
		db.Mutex.Unlock()
	}
}

// Del - Removes record with a given key.
func (db *DB) Del(key KeyType) {
	//println("del", hex.EncodeToString(key[:]))
	db.Mutex.Lock()
	db.Idx.memdel(key)
	if db.VolatileMode {
		db.NoSyncMode = true
		db.Mutex.Unlock()
		return
	}
	db.PendingRecords[key] = true
	if db.syncneeded() {
		go func() {
			db.sync()
			db.Mutex.Unlock()
		}()
	} else {
		db.Mutex.Unlock()
	}
}

// ApplyFlags -
func (db *DB) ApplyFlags(key KeyType, fl uint32) {
	db.Mutex.Lock()
	if idx := db.Idx.get(key); idx != nil {
		idx.applyBrowsingFlags(fl)
	}
	db.Mutex.Unlock()
}

// Defrag - Defragments the DB on the disk.
// Return true if defrag hes been performed, and false if was not needed.
func (db *DB) Defrag(force bool) (doing bool) {
	if db.VolatileMode {
		return
	}
	db.Mutex.Lock()
	doing = force || db.Idx.ExtraSpaceUsed > (uint64(db.O.DefragPercentVal)*db.Idx.DiskSpaceNeeded/100)
	if doing {
		cnt("DefragYes")
		go func() {
			db.defrag()
			db.Mutex.Unlock()
		}()
	} else {
		cnt("DefragNo")
		db.Mutex.Unlock()
	}
	return
}

// NoSync - Disable writing changes to disk.
func (db *DB) NoSync() {
	if db.VolatileMode {
		return
	}
	db.Mutex.Lock()
	db.NoSyncMode = true
	db.Mutex.Unlock()
}

// Sync - Write all the pending changes to disk now.
// Re enable syncing if it has been disabled.
func (db *DB) Sync() {
	if db.VolatileMode {
		return
	}
	db.Mutex.Lock()
	db.NoSyncMode = false
	go func() {
		db.sync()
		db.Mutex.Unlock()
	}()
}

// Close the database.
// Writes all the pending changes to disk.
func (db *DB) Close() {
	db.Mutex.Lock()
	if db.VolatileMode {
		// flush all the data to disk when closing
		if db.NoSyncMode {
			db.defrag()
		}
	} else {
		db.sync()
	}
	if db.LogFile != nil {
		db.LogFile.Close()
		db.LogFile = nil
	}
	db.Idx.close()
	db.Idx = nil
	for _, f := range db.DatFiles {
		f.Close()
	}
	db.Mutex.Unlock()
}

// Flush -
func (db *DB) Flush() {
	if db.VolatileMode {
		return
	}
	cnt("Flush")
	if db.LogFile != nil {
		db.LogFile.Sync()
	}
	if db.Idx.file != nil {
		db.Idx.file.Sync()
	}
}

func (db *DB) defrag() {
	db.DataSeq++
	if db.LogFile != nil {
		db.LogFile.Close()
		db.LogFile = nil
	}
	db.checklogfile()
	bufile := bufio.NewWriterSize(db.LogFile, 0x100000)
	used := make(map[uint32]bool, 10)
	db.Idx.browse(func(key KeyType, rec *oneIdx) bool {
		db.loadrec(rec)
		rec.datpos = uint32(db.addtolog(bufile, key, rec.Slice()))
		rec.DataSeq = db.DataSeq
		used[rec.DataSeq] = true
		rec.freerec()
		return true
	})

	// first write & flush the data file:
	bufile.Flush()
	db.LogFile.Sync()

	// now the index:
	db.Idx.writedatfile() // this will close the file

	db.cleanupold(used)
	db.Idx.ExtraSpaceUsed = 0
}

func (db *DB) sync() {
	if db.VolatileMode {
		return
	}
	if len(db.PendingRecords) > 0 {
		cnt("SyncOK")
		bidx := new(bytes.Buffer)
		db.checklogfile()
		for k := range db.PendingRecords {
			rec := db.Idx.get(k)
			if rec != nil {
				fpos := db.addtolog(nil, k, rec.Slice())
				//rec.datlen = uint32(len(rec.data))
				rec.datpos = uint32(fpos)
				rec.DataSeq = db.DataSeq
				db.Idx.addtolog(bidx, k, rec)
				if (rec.flags & NoCache) != 0 {
					rec.FreeData()
				}
			} else {
				db.Idx.deltolog(bidx, k)
			}
		}
		db.Idx.writebuf(bidx.Bytes())
		db.PendingRecords = make(map[KeyType]bool, db.O.MaxPending)

		if db.Idx.ExtraSpaceUsed > (uint64(db.O.ForcedDefragPerc) * db.Idx.DiskSpaceNeeded / 100) {
			cnt("DefragNow")
			db.defrag()
		}
	} else {
		cnt("SyncNO")
	}
}

func (db *DB) syncneeded() bool {
	if db.VolatileMode {
		return false
	}
	if len(db.PendingRecords) > int(db.O.MaxPendingNoSync) {
		cnt("SyncNeedBig")
		return true
	}
	if !db.NoSyncMode && len(db.PendingRecords) > int(db.O.MaxPending) {
		cnt("SyncNeedSmall")
		return true
	}
	return false
}

func (idx *oneIdx) freerec() {
	if (idx.flags & NoCache) != 0 {
		idx.FreeData()
	}
}

func (idx *oneIdx) applyBrowsingFlags(res uint32) {
	if (res & NoBrowse) != 0 {
		idx.flags |= NoBrowse
	} else if (res & YesBrowse) != 0 {
		idx.flags &= ^uint32(NoBrowse)
	}

	if (res & NoCache) != 0 {
		idx.flags |= NoCache
	} else if (res & YesCache) != 0 {
		idx.flags &= ^uint32(NoCache)
	}
}
