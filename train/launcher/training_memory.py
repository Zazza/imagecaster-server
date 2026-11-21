MIN_FREE_VRAM_BYTES = 14 * 1024 ** 3
BYTES_PER_GIB = 1024 ** 3
FULL_OFFLOAD = 1.0
NO_OFFLOAD = 0.0


def configure_memory(config_path):
    import torch
    import yaml

    with open(config_path, encoding="utf-8") as f:
        config = yaml.safe_load(f)
    low_memory = False
    legacy = False
    changed = False
    for process in config["config"]["process"]:
        if process.get("type") != "sd_trainer":
            continue
        device = process.get("device", "cuda:0")
        if not str(device).startswith("cuda"):
            print(f"[train] warning: automatic RAM offloading requires CUDA, got {device}", flush=True)
            continue
        try:
            if not torch.cuda.is_available():
                raise RuntimeError("CUDA unavailable")
            free, total = torch.cuda.mem_get_info(device)
        except (RuntimeError, OSError) as e:
            print(f"[train] warning: cannot measure VRAM; keeping memory settings: {e}", flush=True)
            continue
        offload = free < MIN_FREE_VRAM_BYTES
        model = process["model"]
        model["layer_offloading"] = offload
        percent = FULL_OFFLOAD if offload else NO_OFFLOAD
        model["layer_offloading_transformer_percent"] = percent
        model["layer_offloading_text_encoder_percent"] = percent
        low_memory = low_memory or offload
        legacy = legacy or (offload and (model.get("is_xl") or model.get("is_flux")))
        changed = True
        mode = "RAM offloading" if offload else "GPU"
        print(f"[train] {device}: {free / BYTES_PER_GIB:.2f}/{total / BYTES_PER_GIB:.2f} GiB free; {mode}", flush=True)
    if changed:
        with open(config_path, "w", encoding="utf-8") as f:
            yaml.safe_dump(config, f, sort_keys=False)
    if legacy:
        install_legacy_offloading()
    return low_memory


def install_legacy_offloading():
    import torch
    from diffusers import FluxTransformer2DModel, UNet2DConditionModel
    from transformers import CLIPTextModel, CLIPTextModelWithProjection, T5EncoderModel
    from toolkit.memory_management import MemoryManager
    from toolkit.stable_diffusion_model import _adopt_v2

    def wrap(cls):
        original = cls.to

        def to(module, *args, **kwargs):
            target = kwargs.get("device", args[0] if args else None)
            if not isinstance(target, (str, torch.device)) or torch.device(target).type != "cuda":
                return original(module, *args, **kwargs)
            cpu_args, cpu_kwargs = args, dict(kwargs)
            if args:
                cpu_args = ("cpu", *args[1:])
            else:
                cpu_kwargs["device"] = "cpu"
            original(module, *cpu_args, **cpu_kwargs)
            module = _adopt_v2(module)
            if not hasattr(module, "get_offload_ignore_modules"):
                raise RuntimeError(f"ai-toolkit cannot offload {type(module).__name__}; update ai-toolkit")
            MemoryManager.attach(module, torch.device(target), offload_percent=FULL_OFFLOAD,
                                 ignore_modules=list(module.get_offload_ignore_modules() or []))
            return module

        cls.to = to

    for cls in (FluxTransformer2DModel, UNet2DConditionModel, CLIPTextModel,
                CLIPTextModelWithProjection, T5EncoderModel):
        wrap(cls)
