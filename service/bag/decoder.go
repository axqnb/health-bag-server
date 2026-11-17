package bag

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/foxglove/go-rosbag/ros1msg"
	"health-bag-server/model"
)

// Decode against the embedded ROS schema. Bounds are checked before allocating
// strings/arrays; signed integer fields (for example tag_id=-1) stay signed.
func decodeMessage(fields []ros1msg.Field, payload []byte) ([]byte, bool, error) {
	r := bytes.NewReader(payload)
	decoder := messageDecoder{reader: r}
	value, err := decoder.fields(fields, 0)
	if err != nil {
		return nil, false, err
	}
	if r.Len() != 0 {
		return nil, false, fmt.Errorf("ROS消息末尾存在多余数据")
	}
	result, err := json.Marshal(value)
	return result, decoder.nonFinite, err
}

type messageDecoder struct {
	reader    *bytes.Reader
	nonFinite bool
}

func (d *messageDecoder) fields(fields []ros1msg.Field, depth int) (map[string]interface{}, error) {
	if depth > 64 {
		return nil, fmt.Errorf("ROS消息嵌套层数超过支持范围")
	}
	result := make(map[string]interface{}, len(fields))
	for _, field := range fields {
		value, err := d.value(field.Type, depth+1)
		if err != nil {
			return nil, fmt.Errorf("字段 %s 解析失败：%w", field.Name, err)
		}
		result[field.Name] = value
	}
	return result, nil
}

func (d *messageDecoder) read(size int) ([]byte, error) {
	if size < 0 || size > d.reader.Len() {
		return nil, io.ErrUnexpectedEOF
	}
	value := make([]byte, size)
	_, err := io.ReadFull(d.reader, value)
	return value, err
}

func (d *messageDecoder) u32() (uint32, error) {
	value, err := d.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}

func minimumSize(typ ros1msg.Type, depth int) int64 {
	if depth > 64 {
		return math.MaxInt32
	}
	if typ.IsArray {
		if typ.FixedSize == 0 {
			return 4
		}
		if typ.Items == nil {
			return math.MaxInt32
		}
		return int64(typ.FixedSize) * minimumSize(*typ.Items, depth+1)
	}
	if typ.IsRecord {
		var size int64
		for _, field := range typ.Fields {
			size += minimumSize(field.Type, depth+1)
		}
		return size
	}
	switch typ.BaseType {
	case "bool", "uint8", "int8", "byte", "char":
		return 1
	case "uint16", "int16":
		return 2
	case "string", "uint32", "int32", "float32":
		return 4
	default:
		return 8
	}
}

func (d *messageDecoder) value(typ ros1msg.Type, depth int) (interface{}, error) {
	if depth > 64 {
		return nil, fmt.Errorf("ROS消息嵌套层数超过支持范围")
	}
	if typ.IsArray {
		if typ.Items == nil {
			return nil, fmt.Errorf("ROS数组缺少元素类型定义")
		}
		count := uint32(typ.FixedSize)
		if typ.FixedSize == 0 {
			var err error
			count, err = d.u32()
			if err != nil {
				return nil, err
			}
		}
		if count > 1000000 || int64(count)*minimumSize(*typ.Items, depth+1) > int64(d.reader.Len()) {
			return nil, fmt.Errorf("ROS数组长度无效或超过上限：%d", count)
		}
		values := make([]interface{}, int(count))
		for i := range values {
			value, err := d.value(*typ.Items, depth+1)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	}
	if typ.IsRecord {
		return d.fields(typ.Fields, depth+1)
	}
	if typ.BaseType == "string" {
		size, err := d.u32()
		if err != nil {
			return nil, err
		}
		value, err := d.read(int(size))
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(value) {
			return nil, fmt.Errorf("ROS字符串不是有效的UTF-8编码")
		}
		return string(value), nil
	}
	size := int(minimumSize(typ, depth))
	data, err := d.read(size)
	if err != nil {
		return nil, err
	}
	switch typ.BaseType {
	case "bool":
		if data[0] > 1 {
			return nil, fmt.Errorf("ROS布尔值无效")
		}
		return data[0] == 1, nil
	case "uint8", "char":
		return data[0], nil
	case "int8", "byte":
		return int8(data[0]), nil
	case "uint16":
		return binary.LittleEndian.Uint16(data), nil
	case "int16":
		return int16(binary.LittleEndian.Uint16(data)), nil
	case "uint32":
		return binary.LittleEndian.Uint32(data), nil
	case "int32":
		return int32(binary.LittleEndian.Uint32(data)), nil
	case "uint64":
		return binary.LittleEndian.Uint64(data), nil
	case "int64":
		return int64(binary.LittleEndian.Uint64(data)), nil
	case "float32":
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(data)))
		if !model.Finite(value) {
			d.nonFinite = true
			return nil, nil
		}
		return value, nil
	case "float64":
		value := math.Float64frombits(binary.LittleEndian.Uint64(data))
		if !model.Finite(value) {
			d.nonFinite = true
			return nil, nil
		}
		return value, nil
	case "time":
		seconds, nanos := binary.LittleEndian.Uint32(data), binary.LittleEndian.Uint32(data[4:])
		if nanos >= 1000000000 {
			return nil, fmt.Errorf("ROS时间的纳秒部分无效")
		}
		return json.Number(fmt.Sprintf("%d.%09d", seconds, nanos)), nil
	case "duration":
		seconds, nanos := int32(binary.LittleEndian.Uint32(data)), int32(binary.LittleEndian.Uint32(data[4:]))
		total := int64(seconds)*1000000000 + int64(nanos)
		sign := ""
		if total < 0 {
			sign = "-"
			total = -total
		}
		return json.Number(sign + model.FormatSeconds(total)), nil
	}
	return nil, fmt.Errorf("暂不支持ROS基础类型 %s", typ.BaseType)
}
