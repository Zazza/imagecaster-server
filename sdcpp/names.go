package sdcpp

import "strings"

var samplers = []struct{ forge, sdcpp string }{
	{"Euler", "euler"},
	{"Euler a", "euler_a"},
	{"Heun", "heun"},
	{"DPM2", "dpm2"},
	{"DPM++ 2S a", "dpm++2s_a"},
	{"DPM++ 2M", "dpm++2m"},
	{"DPM++ 2M SDE", "dpm++2m_sde"},
	{"LMS", "lms"},
	{"LCM", "lcm"},
	{"DDIM", "ddim_trailing"},
	{"TCD", "tcd"},
	{"iPNDM", "ipndm"},
	{"iPNDM_v", "ipndm_v"},
	{"Res Multistep", "res_multistep"},
	{"ER SDE", "er_sde"},
}

var schedulers = []struct{ forge, sdcpp string }{
	{"Simple", "simple"},
	{"Karras", "karras"},
	{"Exponential", "exponential"},
	{"Normal", "normal"},
	{"Uniform", "discrete"},
	{"SGM Uniform", "sgm_uniform"},
	{"Align Your Steps", "ays"},
	{"Align Your Steps GITS", "gits"},
	{"KL Optimal", "kl_optimal"},
	{"Beta", "beta"},
	{"Smoothstep", "smoothstep"},
	{"LCM", "lcm"},
	{"Bong Tangent", "bong_tangent"},
}

func lookupName(table []struct{ forge, sdcpp string }, name string) (string, bool) {
	n := strings.TrimSpace(name)
	for _, e := range table {
		if strings.EqualFold(e.forge, n) || e.sdcpp == strings.ToLower(n) {
			return e.sdcpp, true
		}
	}
	return "", false
}

func SamplerName(forge string) (string, bool) {
	if strings.TrimSpace(forge) == "" {
		return "", true
	}
	return lookupName(samplers, forge)
}

func SplitSampler(forge string) (string, string, bool) {
	if s, ok := SamplerName(forge); ok {
		return s, "", true
	}
	n := strings.TrimSpace(forge)
	for _, sc := range schedulers {
		suffix := " " + sc.forge
		if len(n) > len(suffix) && strings.EqualFold(n[len(n)-len(suffix):], suffix) {
			if s, ok := SamplerName(n[:len(n)-len(suffix)]); ok && s != "" {
				return s, sc.sdcpp, true
			}
		}
	}
	return "", "", false
}

func SchedulerName(forge string) (string, bool) {
	n := strings.TrimSpace(forge)
	if n == "" || strings.EqualFold(n, "Automatic") {
		return "", true
	}
	return lookupName(schedulers, n)
}

var upscalerAliases = map[string]string{
	"R-ESRGAN 4x+":         "RealESRGAN_x4plus",
	"R-ESRGAN 4x+ Anime6B": "RealESRGAN_x4plus_anime_6B",
}

func isBuiltinUpscaler(name string) bool {
	switch name {
	case "None", "Lanczos", "Nearest":
		return true
	}
	return false
}
