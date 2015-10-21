package main

import (
	"bufio"
	"bytes"
	log "github.com/golang/glog"
	"io"
	"os"
)

const (
	// offset
	superBlockHeaderOffset = 8
	// size
	superBlockHeaderSize  = 8
	superBlockMagicSize   = 4
	superBlockVerSize     = 1
	superBlockPaddingSize = superBlockHeaderSize - superBlockMagicSize - superBlockVerSize
	// offset
	superBlockMagicOffset   = 0
	superBlockVerOffset     = superBlockMagicOffset + superBlockVerSize
	superBlockPaddingOffset = superBlockVerOffset + superBlockPaddingSize
	// ver
	superBlockVer1 = byte(1)
	// limits
	// 32GB, offset aligned 8 bytes, 4GB * 8
	superBlockMaxSize   = 4 * 1024 * 1024 * 1024 * 8
	superBlockMaxOffset = 4294967295
)

var (
	superBlockMagic   = []byte{0xab, 0xcd, 0xef, 0x00}
	superBlockVer     = []byte{superBlockVer1}
	superBlockPadding = []byte{0x00, 0x00, 0x00}
)

// An Volume contains one superblock and many needles.
type SuperBlock struct {
	r      *os.File
	w      *os.File
	bw     *bufio.Writer
	File   string
	offset uint32
	Magic  []byte
	Ver    byte
	buf    [NeedleMaxSize]byte
}

// NewSuperBlock new a super block struct.
func NewSuperBlock(file string) (b *SuperBlock, err error) {
	b = &SuperBlock{}
	b.File = file
	if b.w, err = os.OpenFile(file, os.O_WRONLY|os.O_CREATE, 0664); err != nil {
		log.Errorf("os.OpenFile(\"%s\", os.O_WRONLY|os.O_CREATE, 0664) error(%v)", file, err)
		return
	}
	if b.r, err = os.OpenFile(file, os.O_RDONLY, 0664); err != nil {
		log.Errorf("os.OpenFile(\"%s\", os.O_RDONLY, 0664) error(%v)", file, err)
		goto failed
	}
	if err = b.init(); err != nil {
		log.Errorf("block: %s init error(%v)", file, err)
		goto failed
	}
	b.bw = bufio.NewWriterSize(b.w, NeedleMaxSize)
	return
failed:
	if b.w != nil {
		b.w.Close()
	}
	if b.r != nil {
		b.r.Close()
	}
	return
}

// init block file, add/parse meta info
func (b *SuperBlock) init() (err error) {
	var (
		stat os.FileInfo
	)
	if stat, err = b.r.Stat(); err != nil {
		return
	}
	// new file
	if stat.Size() == 0 {
		// magic
		if _, err = b.w.Write(superBlockMagic); err != nil {
			return
		}
		// ver
		if _, err = b.w.Write(superBlockVer); err != nil {
			return
		}
		// padding
		if _, err = b.w.Write(superBlockPadding); err != nil {
			return
		}
	} else {
		if _, err = b.r.Read(b.buf[:superBlockHeaderSize]); err != nil {
			return
		}
		// parse block meta data
		// check magic
		b.Magic = b.buf[superBlockMagicOffset : superBlockMagicOffset+superBlockMagicSize]
		b.Ver = byte(b.buf[superBlockVerOffset : superBlockVerOffset+superBlockVerSize][0])
		if !bytes.Equal(b.Magic, superBlockMagic) {
			err = ErrSuperBlockMagic
			return
		}
		if b.Ver == superBlockVer1 {
			err = ErrSuperBlockVer
			return
		}
		if _, err = b.w.Seek(superBlockHeaderOffset, os.SEEK_SET); err != nil {
			return
		}
	}
	b.offset = NeedleOffset(superBlockHeaderOffset)
	return
}

// Add append a photo to the block.
func (b *SuperBlock) Add(key, cookie int64, data []byte) (offset uint32, size int32, err error) {
	var (
		padding    int32
		incrOffset uint32
		dataSize   = int32(len(data))
	)
	padding, size = NeedleSize(dataSize)
	incrOffset = NeedleOffset(size)
	if superBlockMaxOffset-incrOffset < b.offset {
		err = ErrSuperBlockNoSpace
		return
	}
	FillNeedle(padding, dataSize, key, cookie, data, b.buf[:])
	if _, err = b.w.Write(b.buf[:size]); err != nil {
		return
	}
	offset = b.offset
	b.offset += incrOffset
	// TODO append N times call flush then clean the os page cache
	// page cache no used here...
	// after upload a photo, we cache in user-level.
	log.V(1).Infof("add a needle, cur offset: %d", b.offset)
	return
}

// Write start add needles to the block, must called after start a transaction.
func (b *SuperBlock) Write(key, cookie int64, data []byte) (offset uint32, size int32, err error) {
	var (
		padding    int32
		incrOffset uint32
		dataSize   = int32(len(data))
	)
	padding, size = NeedleSize(dataSize)
	incrOffset = NeedleOffset(size)
	if superBlockMaxOffset-incrOffset < b.offset {
		err = ErrSuperBlockNoSpace
		return
	}
	FillNeedle(padding, dataSize, key, cookie, data, b.buf[:])
	if _, err = b.bw.Write(b.buf[:size]); err != nil {
		return
	}
	offset = b.offset
	// WARN b.offset is dirty data here
	b.offset += incrOffset
	return
}

// Flush flush writer buffer.
func (b *SuperBlock) Flush() (err error) {
	if err = b.bw.Flush(); err != nil {
		return
	}
	// TODO append N times call flush then clean the os page cache
	// page cache no used here...
	// after upload a photo, we cache in user-level.
	return
}

// Repair repair the specified offset needle without update current offset.
func (b *SuperBlock) Repair(key, cookie int64, data []byte, offset uint32) (err error) {
	var (
		size     int32
		padding  int32
		dataSize = int32(len(data))
	)
	padding, size = NeedleSize(dataSize)
	FillNeedle(padding, dataSize, key, cookie, data, b.buf[:])
	if _, err = b.w.WriteAt(b.buf[:size], BlockOffset(offset)); err != nil {
		return
	}
	// TODO append N times call flush then clean the os page cache
	// page cache no used here...
	// after upload a photo, we cache in user-level.
	return
}

// Get get a needle from super block.
func (b *SuperBlock) Get(offset uint32, buf []byte) (err error) {
	if _, err = b.r.ReadAt(buf, BlockOffset(offset)); err != nil {
		return
	}
	return
}

// Del logical del a needls, only update the flag to it.
func (b *SuperBlock) Del(offset uint32) (err error) {
	// WriteAt won't update the file offset.
	if _, err = b.w.WriteAt(NeedleStatusDelBytes, BlockOffset(offset)+NeedleFlagOffset); err != nil {
		return
	}
	return
}

// Dump parse supper block file and dump print for debug.
// ONLY DEBUG!!!!
func (b *SuperBlock) Dump() (err error) {
	var (
		rd   *bufio.Reader
		data []byte
		n    = &Needle{}
	)
	if _, err = b.r.Seek(0, os.SEEK_SET); err != nil {
		return
	}
	rd = bufio.NewReaderSize(b.r, NeedleMaxSize)
	for {
		// header
		if data, err = rd.Peek(NeedleHeaderSize); err != nil {
			break
		}
		if err = n.ParseHeader(data); err != nil {
			break
		}
		if _, err = rd.Discard(NeedleHeaderSize); err != nil {
			break
		}
		// data
		if data, err = rd.Peek(n.DataSize); err != nil {
			break
		}
		if err = n.ParseData(data); err != nil {
			break
		}
		if _, err = rd.Discard(n.DataSize); err != nil {
			break
		}
		log.Info(n.String())
	}
	if err == io.EOF {
		err = nil
	}
	return
}

// Recovery recovery needles map from super block.
func (b *SuperBlock) Recovery(needles map[int64]NeedleCache, indexer *Indexer, offset int64) (err error) {
	var (
		size    int32
		data    []byte
		rd      *bufio.Reader
		n       = &Needle{}
		nc      NeedleCache
		noffset uint32
	)
	log.Infof("start super block recovery, offset: %d\n", offset)
	if offset == 0 {
		offset = superBlockHeaderOffset
		noffset = NeedleOffset(superBlockHeaderOffset)
	}
	if _, err = b.r.Seek(offset, os.SEEK_SET); err != nil {
		log.Errorf("block: %s seek error(%v)", b.File)
		return
	}
	rd = bufio.NewReaderSize(b.r, NeedleMaxSize)
	for {
		// header
		if data, err = rd.Peek(NeedleHeaderSize); err != nil {
			break
		}
		if err = n.ParseHeader(data); err != nil {
			break
		}
		if _, err = rd.Discard(NeedleHeaderSize); err != nil {
			break
		}
		// data
		if data, err = rd.Peek(n.DataSize); err != nil {
			break
		}
		if err = n.ParseData(data); err != nil {
			break
		}
		if _, err = rd.Discard(n.DataSize); err != nil {
			break
		}
		size = int32(NeedleHeaderSize + n.DataSize)
		if n.Flag == NeedleStatusOK {
			if err = indexer.Add(n.Key, noffset, size); err != nil {
				break
			}
			nc = NewNeedleCache(noffset, size)
		} else {
			nc = NewNeedleCache(NeedleCacheDelOffset, size)
		}
		needles[n.Key] = nc
		log.V(1).Infof("recovery needle: offset: %d, size: %d", noffset, size)
		log.V(1).Info(n.String())
		noffset += NeedleOffset(size)
	}
	if err == io.EOF {
		err = nil
	}
	// reset b.w offset, discard left space which can't parse to a needle
	if _, err = b.w.Seek(BlockOffset(noffset), os.SEEK_SET); err != nil {
		log.Errorf("reset block: %s offset error(%v)", b.File, err)
		return
	}
	return
}

// Compress compress the orig block, copy to disk dst block.
func (b *SuperBlock) Compress(v *Volume) (err error) {
	var (
		data []byte
		r    *os.File
		rd   *bufio.Reader
		n    = &Needle{}
	)
	log.Infof("start super block compress: %s\n", b.File)
	if r, err = os.OpenFile(b.File, os.O_RDONLY, 0664); err != nil {
		log.Errorf("os.OpenFile(\"%s\", os.O_RDONLY, 0664) error(%v)", b.File, err)
		return
	}
	if _, err = r.Seek(superBlockHeaderOffset, os.SEEK_SET); err != nil {
		return
	}
	rd = bufio.NewReaderSize(r, NeedleMaxSize)
	for {
		// header
		if data, err = rd.Peek(NeedleHeaderSize); err != nil {
			break
		}
		if err = n.ParseHeader(data); err != nil {
			break
		}
		if _, err = rd.Discard(NeedleHeaderSize); err != nil {
			break
		}
		// data
		if data, err = rd.Peek(n.DataSize); err != nil {
			break
		}
		if err = n.ParseData(data); err != nil {
			break
		}
		if _, err = rd.Discard(n.DataSize); err != nil {
			break
		}
		log.V(1).Info(n.String())
		// skip delete needle
		if n.Flag == NeedleStatusDel {
			continue
		}
		// multi append
		if err = v.Write(n.Key, n.Cookie, n.Data); err != nil {
			break
		}
	}
	if err != io.EOF {
		return
	}
	if err = v.Flush(); err != nil {
		return
	}
	if err = r.Close(); err != nil {
		return
	}
	return
}

func (b *SuperBlock) Close() {
	var err error
	if err = b.bw.Flush(); err != nil {
		log.Errorf("block: %s flush error(%v)", b.File, err)
	}
	if err = b.w.Sync(); err != nil {
		log.Errorf("block: %s sync error(%v)", b.File, err)
	}
	if err = b.w.Close(); err != nil {
		log.Errorf("block: %s close error(%v)", b.File, err)
	}
	if err = b.r.Close(); err != nil {
		log.Errorf("block: %s close error(%v)", b.File, err)
	}
	return
}

// BlockOffset get super block file offset.
func BlockOffset(offset uint32) int64 {
	return int64(offset) * NeedlePaddingSize
}