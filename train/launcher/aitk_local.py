import json
import os
import runpy
import sys

toolkit_dir, config_path, local_path = sys.argv[1], sys.argv[2], sys.argv[3]
with open(local_path, encoding="utf-8") as f:
    local = json.load(f)
os.chdir(toolkit_dir)
sys.path.insert(0, toolkit_dir)

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

sys.argv = [os.path.join(toolkit_dir, "run.py"), config_path]
runpy.run_path(sys.argv[0], run_name="__main__")
