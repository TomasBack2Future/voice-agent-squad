"""Select an explicitly configured original native client; never substitute one."""
import importlib

from validate_context_package import ValidationError

IDENTITIES = {'codex': 'CODEX_THREAD_ID', 'muse': 'MUSE_SESSION_ID',
              'claude': 'CLAUDE_SESSION_ID'}


def native_identity(config):
    try:
        return IDENTITIES[config['runtime']]
    except KeyError as error:
        raise ValidationError('explicit supported native runtime required') from error


def backend(config):
    native_identity(config)
    if config['runtime'] == 'claude':
        raise ValidationError('current Claude native lane awaits qualification; use the existing legacy route')
    module = {'codex': 'codex_native_host', 'muse': 'muse_current_native'}[config['runtime']]
    return importlib.import_module(module)


def validate_selection(config):
    selected = backend(config)
    if config['runtime'] == 'codex':
        if (config.get('provider') != 'openai' or config.get('approval_policy') != 'never'
                or config.get('sandbox') != 'danger-full-access'):
            raise ValidationError('explicit current Codex policy required')
        return selected.binary_identity(config['client_executable'])
    return selected.validate_selection(config)


def native_host(root, config, mcp=None):
    validate_selection(config)
    return backend(config).NativeCLI(root, config, mcp)
