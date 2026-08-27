package fsprofile

import "testing"

func TestKindForFSType(t *testing.T) {
	tests := []struct {
		fsType string
		want   Kind
	}{
		{"ext4", KindLocal},
		{"overlay", KindLocal},
		{"apfs", KindLocal},
		{"nfs4", KindNetwork},
		{"cifs", KindNetwork},
		{"smb3", KindNetwork},
		{"smbfs", KindNetwork},
		{"fuse.sshfs", KindNetwork},
		{"fuse.rclone", KindNetwork},
		{"fuse.davfs", KindNetwork},
		{"fuse.s3fs", KindNetwork},
		{"fuse", KindNetwork},
		{"fuse.unknown", KindNetwork},
		{"fuseblk", KindLocal},
		{"fuse.ntfs-3g", KindLocal},
		{"fuse.exfat", KindLocal},
		{"fuse.mergerfs", KindLocal},
		{"fuse.gocryptfs", KindLocal},
		{"fuse.bindfs", KindLocal},
		{"9p", KindNetwork},
		{"webdav", KindNetwork},
		{"afpfs", KindNetwork},
		{"virtiofs", KindLocal},
		{"fuse.glusterfs", KindNetwork},
		{"", KindUnknown},
	}
	for _, tt := range tests {
		if got := kindForFSType(tt.fsType); got != tt.want {
			t.Fatalf("kindForFSType(%q) = %s; want %s", tt.fsType, got, tt.want)
		}
	}
}
