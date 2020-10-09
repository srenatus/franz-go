// Package kmsg contains Kafka request and response types and autogenerated
// serialization and deserialization functions.
//
// This package reserves the right to add new fields to struct types as Kafka
// adds new fields over time without bumping the major version. New requests
// will also be added without bumping the major version. The major version will
// also NOT BE BUMPED if a field's type is changed. The major version of this
// package is only bumped if it is required to be bumped per the kgo package.
//
// Kafka has only once in its history changed a non-array field's type,
// changing a string to a pointer to a string. These types of changes are
// expected to be very uncommon, and this package is provided with the
// understanding that it is advanced and may require some very minor
// maintenance if a field's type changes.
//
// If you are using this package directly with kgo, you should either ALWAYS
// use New functions (or Default functions after creating structs, you should
// pin the max supported version. If you use New functions, you should have
// safe defaults as new fields are added. If you pin versions, you will avoid
// new fields being used. If you do neither of these, you may opt in to new
// fields that do not have safe zero value defaults, and this may lead to
// errors or unexpected results.
//
// All "Default" functions set non-Go-default field defaults. They do not set
// any fields whose default value is a Go default. Thus, Default functions will
// set -1, but not 0 nor nil. All "New" functions also set non-Go-default
// fields. Requests and Responses also have a "NewPtr" function that is the
// same as "New," but returns a pointer to the type.
//
// Most of this package is generated, but a few things are manual. What is
// manual: all interfaces, the RequestFormatter, record / message / record
// batch reading, and sticky member metadata serialization.
package kmsg

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"github.com/twmb/kafka-go/pkg/kbin"
)

// Requestor issues requests. Notably, the kgo.Client and kgo.Broker implements
// Requestor. All Requests in this package have a RequestWith function to have
// type-safe requests.
type Requestor interface {
	// Request issues a Request and returns either a Response or an error.
	Request(context.Context, Request) (Response, error)
}

// Request represents a type that can be requested to Kafka.
type Request interface {
	// Key returns the protocol key for this message kind.
	Key() int16
	// MaxVersion returns the maximum protocol version this message
	// supports.
	//
	// This function allows one to implement a client that chooses message
	// versions based off of the max of a message's max version in the
	// client and the broker's max supported version.
	MaxVersion() int16
	// SetVersion sets the version to use for this request and response.
	SetVersion(int16)
	// GetVersion returns the version currently set to use for the request
	// and response.
	GetVersion() int16
	// IsFlexible returns whether the request at its current version is
	// "flexible" as per the KIP-482.
	IsFlexible() bool
	// AppendTo appends this message in wire protocol form to a slice and
	// returns the slice.
	AppendTo([]byte) []byte
	// ReadFrom parses all of the input slice into the response type.
	//
	// This should return an error if too little data is input.
	ReadFrom([]byte) error
	// ResponseKind returns an empty Response that is expected for
	// this message request.
	ResponseKind() Response
}

// AdminRequest represents a request that must be issued to Kafka controllers.
type AdminRequest interface {
	// IsAdminRequest is a method attached to requests that must be
	// issed to Kafka controllers.
	IsAdminRequest()
	Request
}

// GroupCoordinatorRequest represents a request that must be issued to a
// group coordinator.
type GroupCoordinatorRequest interface {
	// IsGroupCoordinatorRequest is a method attached to requests that
	// must be issued to group coordinators.
	IsGroupCoordinatorRequest()
	Request
}

// TxnCoordinatorRequest represents a request that must be issued to a
// transaction coordinator.
type TxnCoordinatorRequest interface {
	// IsTxnCoordinatorRequest is a method attached to requests that
	// must be issued to transaction coordinators.
	IsTxnCoordinatorRequest()
	Request
}

// Response represents a type that Kafka responds with.
type Response interface {
	// Key returns the protocol key for this message kind.
	Key() int16
	// MaxVersion returns the maximum protocol version this message
	// supports.
	MaxVersion() int16
	// SetVersion sets the version to use for this request and response.
	SetVersion(int16)
	// GetVersion returns the version currently set to use for the request
	// and response.
	GetVersion() int16
	// IsFlexible returns whether the request at its current version is
	// "flexible" as per the KIP-482.
	IsFlexible() bool
	// AppendTo appends this message in wire protocol form to a slice and
	// returns the slice.
	AppendTo([]byte) []byte
	// ReadFrom parses all of the input slice into the response type.
	//
	// This should return an error if too little data is input.
	ReadFrom([]byte) error
	// RequestKind returns an empty Request that is expected for
	// this message request.
	RequestKind() Request
}

// RequestFormatter formats requests.
//
// The default empty struct works correctly, but can be extended with the
// NewRequestFormatter function.
type RequestFormatter struct {
	clientID *string

	initPrincipalName *string
	initClientID      *string
}

// RequestFormatterOpt applys options to a RequestFormatter.
type RequestFormatterOpt interface {
	apply(*RequestFormatter)
}

type formatterOpt struct{ fn func(*RequestFormatter) }

func (opt formatterOpt) apply(f *RequestFormatter) { opt.fn(f) }

// FormatterClientID attaches the given client ID to any issued request,
// minus controlled shutdown v0, which uses its own special format.
func FormatterClientID(id string) RequestFormatterOpt {
	return formatterOpt{func(f *RequestFormatter) { f.clientID = &id }}
}

// FormatterInitialID sets the initial ID of the request.
//
// This function should be used by brokers only and is set when the broker
// redirects a request. See KIP-590 for more detail.
func FormatterInitialID(principalName, clientID string) RequestFormatterOpt {
	return formatterOpt{func(f *RequestFormatter) { f.initPrincipalName, f.initClientID = &principalName, &clientID }}
}

// NewRequestFormatter returns a RequestFormatter with the opts applied.
func NewRequestFormatter(opts ...RequestFormatterOpt) *RequestFormatter {
	a := new(RequestFormatter)
	for _, opt := range opts {
		opt.apply(a)
	}
	return a
}

// AppendRequest appends a full message request to dst, returning the updated
// slice. This message is the full body that needs to be written to issue a
// Kafka request.
func (f *RequestFormatter) AppendRequest(
	dst []byte,
	r Request,
	correlationID int32,
) []byte {
	dst = append(dst, 0, 0, 0, 0) // reserve length
	k := r.Key()
	v := r.GetVersion()
	dst = kbin.AppendInt16(dst, k)
	dst = kbin.AppendInt16(dst, v)
	dst = kbin.AppendInt32(dst, correlationID)
	if k == 7 && v == 0 {
		return dst
	}

	// Even with flexible versions, we do not use a compact client id.
	// Clients issue ApiVersions immediately before knowing the broker
	// version, and old brokers will not be able to understand a compact
	// client id.
	dst = kbin.AppendNullableString(dst, f.clientID)

	// The flexible tags end the request header, and then begins the
	// request body.
	if r.IsFlexible() {
		var numTags uint8
		if f.initPrincipalName != nil {
			numTags += 2
		}
		dst = append(dst, numTags)
		if numTags != 0 {
			if f.initPrincipalName != nil {
				dst = kbin.AppendUvarint(dst, 0)
				dst = kbin.AppendCompactString(dst, *f.initPrincipalName)
				dst = kbin.AppendUvarint(dst, 1)
				dst = kbin.AppendCompactString(dst, *f.initClientID)
			}
		}
	}

	// Now the request body.
	dst = r.AppendTo(dst)

	kbin.AppendInt32(dst[:0], int32(len(dst[4:])))
	return dst
}

// StringPtr is a helper to return a pointer to a string.
func StringPtr(in string) *string {
	return &in
}

// ReadRecords reads n records from in and returns them, returning
// kerr.ErrNotEnoughData if in does not contain enough data.
func ReadRecords(n int, in []byte) ([]Record, error) {
	rs := make([]Record, n)
	for i := 0; i < n; i++ {
		length, used := kbin.Varint(in)
		total := used + int(length)
		if used == 0 || length < 0 || len(in) < total {
			return nil, kbin.ErrNotEnoughData
		}
		if err := (&rs[i]).ReadFrom(in[:total]); err != nil {
			return nil, err
		}
		in = in[total:]
	}
	return rs, nil
}

// ErrEncodedCRCMismatch is returned from reading record batches or message sets when
// any batch or set has an encoded crc that does not match a calculated crc.
var ErrEncodedCRCMismatch = errors.New("encoded crc does not match calculated crc")

// ErrEncodedLengthMismatch is returned from reading record batches or message
// sets when any batch or set has an encoded length that does not match the
// earlier read length of the batch / set.
var ErrEncodedLengthMismatch = errors.New("encoded length does not match read length")

var crc32c = crc32.MakeTable(crc32.Castagnoli) // record crc's use Castagnoli table

// ReadRecordBatches reads as many record batches as possible from in,
// discarding any final trailing record batch. This is intended to be used
// for processing RecordBatches from a FetchResponse, where Kafka, as an
// internal optimization, may include a partial final RecordBatch.
func ReadRecordBatches(in []byte) ([]RecordBatch, error) {
	var bs []RecordBatch
	for len(in) > 12 {
		length := int32(binary.BigEndian.Uint32(in[8:]))
		length += 12
		if len(in) < int(length) {
			return bs, nil
		}

		var b RecordBatch
		if err := b.ReadFrom(in[:length]); err != nil {
			return bs, nil
		}

		if int32(len(in[12:length])) != b.Length {
			return bs, ErrEncodedLengthMismatch
		}

		// If we did not error, the length was at _least_ 21.
		if int32(crc32.Checksum(in[21:length], crc32c)) != b.CRC {
			return bs, ErrEncodedCRCMismatch
		}

		bs = append(bs, b)
		in = in[length:]
	}
	return bs, nil
}

// ReadV1Messages reads as many v1 message sets as possible from
// in, discarding any final trailing message set. This is intended to be used
// for processing v1 MessageSets from a FetchResponse, where Kafka, as an
// internal optimization, may include a partial final MessageSet.
func ReadV1Messages(in []byte) ([]MessageV1, error) {
	var ms []MessageV1
	for len(in) > 12 {
		length := int32(binary.BigEndian.Uint32(in[8:]))
		length += 12
		if len(in) < int(length) {
			return ms, nil
		}
		var m MessageV1
		if err := m.ReadFrom(in[:length]); err != nil {
			return ms, nil
		}
		if int32(len(in[12:length])) != m.MessageSize {
			return ms, ErrEncodedLengthMismatch
		}
		if int32(crc32.ChecksumIEEE(in[16:length])) != m.CRC {
			return ms, ErrEncodedCRCMismatch
		}
		ms = append(ms, m)
		in = in[length:]
	}
	return ms, nil
}

// ReadV0Messages reads as many v0 message sets as possible from
// in, discarding any final trailing message set. This is intended to be used
// for processing v0 MessageSets from a FetchResponse, where Kafka, as an
// internal optimization, may include a partial final MessageSet.
func ReadV0Messages(in []byte) ([]MessageV0, error) {
	var ms []MessageV0
	for len(in) > 12 {
		length := int32(binary.BigEndian.Uint32(in[8:]))
		length += 12
		if len(in) < int(length) {
			return ms, nil
		}
		var m MessageV0
		if err := m.ReadFrom(in[:length]); err != nil {
			return ms, nil
		}
		if int32(len(in[12:length])) != m.MessageSize {
			return ms, ErrEncodedLengthMismatch
		}
		if int32(crc32.ChecksumIEEE(in[16:length])) != m.CRC {
			return ms, ErrEncodedCRCMismatch
		}
		ms = append(ms, m)
		in = in[length:]
	}
	return ms, nil
}

// ReadFrom provides decoding various versions of sticky member metadata. A key
// point of this type is that it does not contain a version number inside it,
// but it is versioned: if decoding v1 fails, this falls back to v0.
func (s *StickyMemberMetadata) ReadFrom(src []byte) error {
	b := kbin.Reader{Src: src}
	for i := b.ArrayLen(); i > 0; i-- {
		var assignment StickyMemberMetadataCurrentAssignment
		assignment.Topic = b.String()
		for i := b.ArrayLen(); i > 0; i-- {
			assignment.Partitions = append(assignment.Partitions, b.Int32())
		}
		s.CurrentAssignment = append(s.CurrentAssignment, assignment)
	}
	if len(b.Src) > 0 {
		s.Generation = b.Int32()
	} else {
		s.Generation = -1
	}
	return b.Complete()
}

// AppendTo provides appending various versions of sticky member metadata to dst.
// If generation is not -1 (default for v0), this appends as version 1.
func (s *StickyMemberMetadata) AppendTo(dst []byte) []byte {
	dst = kbin.AppendArrayLen(dst, len(s.CurrentAssignment))
	for _, assignment := range s.CurrentAssignment {
		dst = kbin.AppendString(dst, assignment.Topic)
		dst = kbin.AppendArrayLen(dst, len(assignment.Partitions))
		for _, partition := range assignment.Partitions {
			dst = kbin.AppendInt32(dst, partition)
		}
	}
	if s.Generation != -1 {
		dst = kbin.AppendInt32(dst, s.Generation)
	}
	return dst
}

// SkipTags skips tags in a reader.
func SkipTags(b *kbin.Reader) {
	for num := b.Uvarint(); num > 0; num-- {
		_, size := b.Uvarint(), b.Uvarint()
		b.Span(int(size))
	}
}
