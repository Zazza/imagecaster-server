package installer

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSDCppArtifactsOnnxMirrorAndTrustedPins(t *testing.T) {
	const (
		expectedMirror = "https://registry.npmmirror.com/-/binary/onnxruntime/v1.31.1/"
		expectedDest   = "engines/onnxruntime"
		expectedStrip  = 1
	)
	tests := []struct {
		goos, goarch string
		want         Artifact
	}{
		{
			goos: "windows", goarch: "amd64",
			want: Artifact{
				Name:   "onnxruntime-win-x64-1.31.1.zip",
				SHA256: "5869e26bb25314375f2d5ef1877e3ea06b1543d6c710e6e4a1ec2f7177ab4b2c",
				Size:   82929944, Kind: "zip", Dest: expectedDest, Strip: expectedStrip,
			},
		},
		{
			goos: "darwin", goarch: "arm64",
			want: Artifact{
				Name:   "onnxruntime-osx-arm64-1.31.1.tgz",
				SHA256: "2e1344a1926ee50eb25b38153cb6992c3567515aef0e677a1a520a1798896d6f",
				Size:   36936435, Kind: "tgz", Dest: expectedDest, Strip: expectedStrip,
			},
		},
		{
			goos: "linux", goarch: "amd64",
			want: Artifact{
				Name:   "onnxruntime-linux-x64-1.31.1.tgz",
				SHA256: "c849e36a1ef3e22d4ff217b9d1ce4a3cf5b9467b975610913ae959ad5b16bdc9",
				Size:   11413655, Kind: "tgz", Dest: expectedDest, Strip: expectedStrip,
			},
		},
	}
	for _, tt := range tests {
		for _, gpu := range []GPUKind{GPUNone, GPUNvidia, GPUVulkan} {
			t.Run(fmt.Sprintf("%s/%s/gpu-%d", tt.goos, tt.goarch, gpu), func(t *testing.T) {
				artifacts, err := SDCppArtifacts(tt.goos, tt.goarch, gpu)
				require.NoError(t, err)
				var onnx []Artifact
				for _, artifact := range artifacts {
					if artifact.Dest == expectedDest {
						onnx = append(onnx, artifact)
					}
				}
				want := tt.want
				want.URL = expectedMirror + want.Name
				require.Equal(t, []Artifact{want}, onnx)
			})
		}
	}
}
