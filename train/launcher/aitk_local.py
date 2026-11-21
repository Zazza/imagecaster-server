import json
import os
import runpy
import sys

toolkit_dir, config_path, local_path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(local_path, encoding="utf-8") as f:
    local = json.load(f)
os.chdir(toolkit_dir)
sys.path.insert(0, toolkit_dir)
if local.get("aio"):
    os.environ.setdefault("PYTORCH_CUDA_ALLOC_CONF", "expandable_segments:True")

from training_memory import configure_memory

configure_memory(config_path)

from toolkit.models.v2 import _mixin
from toolkit.models.v2.text_encoders.qwen3 import Qwen3TextEncoder
from toolkit.models.v2.vae.autoencoder_kl import KLVAE

te_file = local.get("te")
vae_file = local.get("vae")
vae_config = None
_load_file = _mixin.load_file


def load_file(path, *args, **kwargs):
    sd = _load_file(path, *args, **kwargs)
    if te_file and path == te_file and "lm_head.weight" not in sd and "model.embed_tokens.weight" in sd:
        sd["lm_head.weight"] = sd["model.embed_tokens.weight"]
    if vae_file and path == vae_file and vae_config is not None and "encoder.down.0.block.0.conv1.weight" in sd:
        from diffusers.loaders.single_file_utils import convert_ldm_vae_checkpoint
        sd = convert_ldm_vae_checkpoint(sd, vae_config)
    return sd


_mixin.load_file = load_file


def local_loader(cls_obj, method, file_path):
    original = getattr(cls_obj, method).__func__

    def load(cls, name_or_path, *args, **kwargs):
        global vae_config
        kwargs["config_path"] = name_or_path
        kwargs.setdefault("subfolder", cls.aitk_subfolder)
        if cls is KLVAE:
            vae_config = cls.load_config(name_or_path, subfolder=kwargs["subfolder"])
        return original(cls, file_path, *args, **kwargs)

    setattr(cls_obj, method, classmethod(load))


if te_file:
    local_loader(Qwen3TextEncoder, "load", te_file)
if vae_file:
    local_loader(KLVAE, "load_model", vae_file)

def flux1_local(aio, cfg_dir):
    import torch
    from accelerate import init_empty_weights
    from safetensors import safe_open
    from diffusers import AutoencoderKL, FluxTransformer2DModel
    from diffusers.loaders.single_file_utils import convert_flux_transformer_checkpoint_to_diffusers, convert_ldm_vae_checkpoint
    from transformers import CLIPTextConfig, CLIPTextModel, T5Config, T5EncoderModel

    cfg_dir = os.path.abspath(cfg_dir)

    def part(prefix, dtype):
        out = {}
        with safe_open(aio, framework="pt") as f:
            for k in f.keys():
                if k.startswith(prefix):
                    out[k[len(prefix):]] = f.get_tensor(k).to(dtype)
        return out

    def ours(path, subfolder):
        p = os.path.abspath(os.path.join(path, subfolder) if subfolder else path)
        return p.startswith(cfg_dir)

    def fill(model, sd, name):
        missing, unexpected = model.load_state_dict(sd, strict=False, assign=True)
        if missing:
            raise RuntimeError(f"{name}: {len(missing)} weights missing in {aio}, e.g. {missing[:3]}")
        print(f"flux1 {name}: {len(sd)} tensors, unexpected {len(unexpected)}", flush=True)
        return model

    def patch(cls, build):
        orig = cls.from_pretrained.__func__

        def from_pretrained(c, path, *a, subfolder=None, torch_dtype=None, **kw):
            if c is cls and ours(path, subfolder):
                return build(os.path.join(path, subfolder) if subfolder else path, torch_dtype or torch.bfloat16)
            return orig(c, path, *a, subfolder=subfolder, torch_dtype=torch_dtype, **kw)

        cls.from_pretrained = classmethod(from_pretrained)

    def cpu_only(m):
        orig_to, orig_forward = m.to, m.forward

        def to(*a, **kw):
            a = tuple(x for x in a if not (isinstance(x, (str, torch.device)) or x is None))
            kw.pop("device", None)
            return orig_to(*a, **kw) if a or kw else m

        def forward(*a, **kw):
            dev = None

            def mv(x):
                nonlocal dev
                if torch.is_tensor(x):
                    dev = dev or x.device
                    return x.to("cpu")
                return x

            a = tuple(mv(x) for x in a)
            kw = {k: mv(v) for k, v in kw.items()}
            with torch.no_grad():
                out = orig_forward(*a, **kw)
            if dev is not None:
                for k in list(out.keys()):
                    if torch.is_tensor(out[k]):
                        out[k] = out[k].to(dev)
            return out

        m.to, m.forward = to, forward
        return m

    def transformer(path, dtype):
        sd = convert_flux_transformer_checkpoint_to_diffusers(part("model.diffusion_model.", dtype))
        with init_empty_weights():
            m = FluxTransformer2DModel.from_config(FluxTransformer2DModel.load_config(path))
        return fill(m, sd, "transformer").to(dtype)

    def vae(path, dtype):
        cfg = AutoencoderKL.load_config(path)
        sd = convert_ldm_vae_checkpoint(part("vae.", torch.float32), cfg)
        with init_empty_weights():
            m = AutoencoderKL.from_config(cfg)
        return fill(m, sd, "vae").to(dtype)

    def t5(path, dtype):
        sd = part("text_encoders.t5xxl.transformer.", dtype)
        sd.setdefault("encoder.embed_tokens.weight", sd["shared.weight"])
        with init_empty_weights():
            m = T5EncoderModel(T5Config.from_pretrained(path))
        m = fill(m, sd, "t5").to(dtype)
        m.requires_grad_(False)
        m.eval()
        return cpu_only(m)

    def clip(path, dtype):
        sd = part("text_encoders.clip_l.transformer.", dtype)
        sd.pop("text_projection.weight", None)
        with init_empty_weights():
            m = CLIPTextModel(CLIPTextConfig.from_pretrained(path))
        return fill(m, sd, "clip").to(dtype)

    patch(FluxTransformer2DModel, transformer)
    patch(AutoencoderKL, vae)
    patch(T5EncoderModel, t5)
    patch(CLIPTextModel, clip)


if local.get("aio"):
    flux1_local(local["aio"], local["flux1_config"])

sys.argv = [os.path.join(toolkit_dir, "run.py"), config_path]
runpy.run_path(sys.argv[0], run_name="__main__")
