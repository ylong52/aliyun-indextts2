from indextts.cache_utils import CACHE_DIR, hf_cached_download


def load_custom_model_from_hf(repo_id, model_filename="pytorch_model.bin", config_filename="config.yml"):
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    model_path = hf_cached_download(repo_id=repo_id, filename=model_filename)
    if config_filename is None:
        return model_path
    config_path = hf_cached_download(repo_id=repo_id, filename=config_filename)

    return model_path, config_path