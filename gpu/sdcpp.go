package gpu

import (
	"slices"
	"strconv"
)

const (
	sdcppFullVRAMMB    = 15 * 1024
	sdcppReserveGiB    = 3
	sdcppMaxVRAMFlag   = "--max-vram"
	sdcppAutoFitFlag   = "--auto-fit"
	sdcppParamsBackend = "--params-backend"
	sdcppOffloadFlag   = "--offload-to-cpu"
)

func SDCppArgsForVRAM(args []string, vramMB int) []string {
	if vramMB <= 0 || vramMB >= sdcppFullVRAMMB {
		return args
	}
	for _, a := range args {
		switch a {
		case sdcppMaxVRAMFlag, sdcppAutoFitFlag, sdcppParamsBackend, sdcppOffloadFlag:
			return args
		}
	}
	return append(slices.Clone(args), sdcppMaxVRAMFlag, "-"+strconv.Itoa(sdcppReserveGiB))
}
