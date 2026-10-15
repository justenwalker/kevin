package output

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestFromProto(t *testing.T) {
	t.Run("is nil without values", func(t *testing.T) {
		assert.Nil(t, FromProto(nil))
		assert.Nil(t, FromProto(&pb.Outputs{}))
	})

	t.Run("keeps the value and its sensitivity", func(t *testing.T) {
		got := FromProto(&pb.Outputs{Values: map[string]*pb.Value{
			"url":   {Kind: &pb.Value_StringValue{StringValue: "http://x"}},
			"token": {Kind: &pb.Value_StringValue{StringValue: "s3"}, Sensitive: true},
		}})

		assert.Equal(t, Map{"url": {String: "http://x"}, "token": {String: "s3", Sensitive: true}}, got)
	})
}

func TestMapToProto(t *testing.T) {
	t.Run("is nil when empty", func(t *testing.T) {
		assert.Nil(t, Map(nil).ToProto())
		assert.Nil(t, Map{}.ToProto())
	})

	t.Run("keeps the value and its sensitivity", func(t *testing.T) {
		got := Map{"k": {String: "v"}, "secret": {String: "s", Sensitive: true}}.ToProto()

		assert.Equal(t, &pb.Outputs{Values: map[string]*pb.Value{
			"k":      {Kind: &pb.Value_StringValue{StringValue: "v"}},
			"secret": {Kind: &pb.Value_StringValue{StringValue: "s"}, Sensitive: true},
		}}, got)
	})
}
