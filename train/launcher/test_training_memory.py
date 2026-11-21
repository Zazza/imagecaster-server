import contextlib
import copy
import importlib.util
import io
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

sys.dont_write_bytecode = True

import yaml

SOURCE = Path(__file__).with_name('training_memory.py')
GIB = 1024 ** 3
KEYS = ('layer_offloading', 'layer_offloading_transformer_percent', 'layer_offloading_text_encoder_percent')
BASES = {'Z-Image': {'arch': 'zimage'}, 'Moss': {'arch': 'moss_image'}, 'Flux2': {'arch': 'flux2_klein_4b'}, 'SDXL': {'is_xl': True}, 'Flux1': {'is_flux': True}}


class TrainingMemoryTests(unittest.TestCase):
    def setUp(self):
        self.cuda = types.SimpleNamespace(is_available=Mock(return_value=True), mem_get_info=Mock(return_value=(8 * GIB, 16 * GIB)))
        self.torch = types.SimpleNamespace(cuda=self.cuda)
        self.fake_torch = patch.dict(sys.modules, {'torch': self.torch})
        self.fake_torch.start()
        self.addCleanup(self.fake_torch.stop)
        spec = importlib.util.spec_from_file_location('training_memory_under_test', SOURCE)
        self.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.mod)
        self.install = Mock()
        self.mod.install_legacy_offloading = self.install
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'config.yaml'
        self.doc = {'job': 'extension', 'config': {'name': 'ada', 'process': [{
            'type': 'sd_trainer', 'device': 'cuda:0', 'model': {},
            'network': {'pretrained_lora_path': '/tmp/prior.safetensors'},
            'datasets': [{'folder_path': '/tmp/photos', 'resolution': [768]}],
            'train': {'steps': 2000},
        }]}}

    def write(self, base='Z-Image', enabled=None):
        model = dict(BASES[base], low_vram=True, quantize=True)
        if enabled is not None:
            model.update(dict(zip(KEYS, (enabled, float(enabled), float(enabled)))))
        self.doc['config']['process'][0]['model'] = model
        self.path.write_text(yaml.safe_dump(self.doc))

    def run_configure(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            result = self.mod.configure_memory(str(self.path))
        return result, out.getvalue()

    def read(self):
        return yaml.safe_load(self.path.read_text())

    def model(self):
        return self.read()['config']['process'][0]['model']

    def test_generic_threshold_constant(self):
        self.assertEqual(self.mod.MIN_FREE_VRAM_BYTES, 14 * GIB)

    def test_all_supported_bases_enable_full_offloading_when_low(self):
        for base in BASES:
            for free in (7 * GIB, 11 * GIB, 14 * GIB - 1):
                with self.subTest(base=base, free=free):
                    self.install.reset_mock()
                    self.write(base)
                    self.cuda.mem_get_info.return_value = (free, 16 * GIB)
                    low, _ = self.run_configure()
                    self.assertIs(low, True)
                    self.assertEqual(tuple(self.model().get(k) for k in KEYS), (True, 1.0, 1.0))
                    self.assertEqual(self.install.call_count, int(base in ('SDXL', 'Flux1')))

    def test_sufficient_free_memory_clears_previous_mode_for_every_base(self):
        for base in BASES:
            for free in (14 * GIB, 16 * GIB):
                with self.subTest(base=base, free=free):
                    self.write(base, enabled=True)
                    self.cuda.mem_get_info.return_value = (free, 24 * GIB)
                    low, _ = self.run_configure()
                    self.assertIs(low, False)
                    self.assertEqual(tuple(self.model().get(k) for k in KEYS), (False, 0.0, 0.0))
                    self.install.assert_not_called()

    def test_probe_uses_actual_training_device(self):
        self.doc['config']['process'][0]['device'] = 'cuda:1'
        self.write()
        self.run_configure()
        self.cuda.mem_get_info.assert_called_once_with('cuda:1')

    def test_non_cuda_preserves_config_and_warns_without_probe(self):
        for device in ('cpu', 'mps'):
            with self.subTest(device=device):
                self.doc['config']['process'][0]['device'] = device
                self.write(enabled=True)
                before = self.path.read_bytes()
                low, output = self.run_configure()
                self.assertIs(low, False)
                self.assertEqual(self.path.read_bytes(), before)
                self.assertIn('warn', output.lower())
                self.cuda.mem_get_info.assert_not_called()
                self.install.assert_not_called()

    def test_unknown_gpu_preserves_config_and_warns(self):
        for available, error in ((False, None), (True, RuntimeError('GPU unavailable'))):
            with self.subTest(available=available):
                self.cuda.is_available.return_value = available
                self.cuda.mem_get_info.side_effect = error
                self.write(enabled=True)
                before = self.path.read_bytes()
                low, output = self.run_configure()
                self.assertIs(low, False)
                self.assertEqual(self.path.read_bytes(), before)
                self.assertIn('warn', output.lower())
                self.install.assert_not_called()

    def test_unrelated_settings_survive(self):
        self.write()
        original = copy.deepcopy(self.doc)
        self.run_configure()
        actual = self.read()
        for key in KEYS:
            actual['config']['process'][0]['model'].pop(key)
        self.assertEqual(actual, original)

    def test_each_launch_recalculates_choice_including_resume(self):
        self.write('SDXL')
        self.cuda.mem_get_info.side_effect = [(7 * GIB, 16 * GIB), (15 * GIB, 16 * GIB)]
        self.assertTrue(self.run_configure()[0])
        self.assertTrue(self.model()['layer_offloading'])
        self.assertFalse(self.run_configure()[0])
        self.assertFalse(self.model()['layer_offloading'])
        self.assertEqual(self.cuda.mem_get_info.call_count, 2)
        self.install.assert_called_once()

    def test_launcher_configures_memory_before_toolkit_model_imports(self):
        import ast
        tree = ast.parse(SOURCE.with_name('aitk_local.py').read_text())
        calls = [n.lineno for n in ast.walk(tree) if isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id == 'configure_memory']
        imports = [n.lineno for n in tree.body if isinstance(n, ast.ImportFrom) and (n.module or '').startswith('toolkit')]
        self.assertEqual(len(calls), 1)
        self.assertTrue(imports)
        self.assertLess(calls[0], min(imports))


if __name__ == '__main__':
    unittest.main()
