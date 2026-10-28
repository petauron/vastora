package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestSenlunVolumeTransferValidatesContentWithoutBackup(t *testing.T) {
	var source bytes.Buffer
	writer := tar.NewWriter(&source)
	if err := writer.WriteHeader(&tar.Header{Name: "senlun/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 65532, Gid: 65532}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "senlun/database", Typeflag: tar.TypeReg, Mode: 0600, Uid: 65532, Gid: 65532, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	before, err := streamSenlunVolume(context.Background(), bytes.NewReader(source.Bytes()), &destination, "senlun")
	if err != nil {
		t.Fatal(err)
	}
	after, err := streamSenlunVolume(context.Background(), bytes.NewReader(destination.Bytes()), nil, "senlun")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("transferred data or ownership differs")
	}
	for _, header := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "senlun/link", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0777},
	} {
		var bad bytes.Buffer
		archive := tar.NewWriter(&bad)
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := streamSenlunVolume(context.Background(), bytes.NewReader(bad.Bytes()), nil, "senlun"); err == nil {
			t.Fatal("accepted an unsafe archive entry")
		}
	}
}
