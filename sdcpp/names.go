package sdcpp

import "slices"

var samplerNames = []string{
	"euler",
	"euler_a",
	"heun",
	"dpm2",
	"dpm++2s_a",
	"dpm++2m",
	"dpm++2m_sde",
	"lms",
	"lcm",
	"ddim_trailing",
	"tcd",
	"ipndm",
	"ipndm_v",
	"res_multistep",
	"er_sde",
}

var schedulerNames = []string{
	"simple",
	"karras",
	"exponential",
	"normal",
	"discrete",
	"sgm_uniform",
	"ays",
	"gits",
	"kl_optimal",
	"beta",
	"smoothstep",
	"lcm",
	"bong_tangent",
}

func validSampler(name string) bool {
	return name == "" || slices.Contains(samplerNames, name)
}

func validScheduler(name string) bool {
	return name == "" || slices.Contains(schedulerNames, name)
}
