package bag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	rosbag "github.com/foxglove/go-rosbag"
)

// Validate outer record lengths before handing data to the bag library, which
// trusts wire lengths. This also rejects an interrupted index/tail upload.
func validateFile(f *os.File, size int64) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	magic := make([]byte, len(rosbag.Magic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, rosbag.Magic) {
		return fmt.Errorf("文件不是有效的ROS1 bag")
	}
	position := int64(len(magic))
	var expectedIndex uint64
	indexFound := false
	chunks := map[uint64]bool{}
	for position < size {
		recordStart := uint64(position)
		var length uint32
		if err := binary.Read(f, binary.LittleEndian, &length); err != nil {
			return err
		}
		position += 4
		if length < 5 || length > 1024*1024 || int64(length)+4 > size-position {
			return fmt.Errorf("bag记录头长度无效")
		}
		header := make([]byte, int(length))
		if _, err := io.ReadFull(f, header); err != nil {
			return err
		}
		position += int64(length)
		fields := map[string][]byte{}
		for offset := 0; offset < len(header); {
			if len(header)-offset < 4 {
				return fmt.Errorf("bag记录头字段不完整")
			}
			n := int(binary.LittleEndian.Uint32(header[offset:]))
			offset += 4
			if n <= 0 || n > len(header)-offset {
				return fmt.Errorf("bag记录头字段长度无效")
			}
			value := header[offset : offset+n]
			offset += n
			separator := bytes.IndexByte(value, '=')
			if separator <= 0 {
				return fmt.Errorf("bag记录头格式无效")
			}
			fields[string(value[:separator])] = value[separator+1:]
		}
		if len(fields["op"]) != 1 {
			return fmt.Errorf("bag记录缺少操作类型")
		}
		if err := binary.Read(f, binary.LittleEndian, &length); err != nil {
			return err
		}
		position += 4
		if int64(length) > size-position {
			return fmt.Errorf("bag记录数据尚不完整，请等待上传完成")
		}
		if length > 64*1024*1024 {
			return fmt.Errorf("bag单条记录超过支持的64 MiB上限")
		}
		op := fields["op"][0]
		if recordStart == uint64(len(magic)) {
			if op != byte(rosbag.OpBagHeader) || len(fields["index_pos"]) != 8 {
				return fmt.Errorf("bag文件头无效")
			}
			expectedIndex = binary.LittleEndian.Uint64(fields["index_pos"])
		}
		if recordStart == expectedIndex && op == byte(rosbag.OpConnection) {
			indexFound = true
		}
		if op == byte(rosbag.OpIndexData) || op == byte(rosbag.OpChunkInfo) {
			if len(fields["count"]) != 4 {
				return fmt.Errorf("bag索引计数无效")
			}
			count := uint64(binary.LittleEndian.Uint32(fields["count"]))
			entrySize := uint64(12)
			if op == byte(rosbag.OpChunkInfo) {
				entrySize = 8
			}
			if count*entrySize != uint64(length) {
				return fmt.Errorf("bag索引条目数量与数据长度不一致")
			}
		}
		if op == byte(rosbag.OpChunkInfo) {
			if len(fields["chunk_pos"]) != 8 || !chunks[binary.LittleEndian.Uint64(fields["chunk_pos"])] {
				return fmt.Errorf("bag数据块索引指向了无效位置")
			}
		}
		if fields["op"][0] == byte(rosbag.OpChunk) {
			chunks[recordStart] = true
			chunkSize := fields["size"]
			if len(chunkSize) != 4 || binary.LittleEndian.Uint32(chunkSize) > 64*1024*1024 {
				return fmt.Errorf("bag数据块超过支持的64 MiB上限")
			}
		}
		if _, err := f.Seek(int64(length), io.SeekCurrent); err != nil {
			return err
		}
		position += int64(length)
	}
	if expectedIndex == 0 || !indexFound {
		return fmt.Errorf("bag索引缺失或索引位置无效")
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}
