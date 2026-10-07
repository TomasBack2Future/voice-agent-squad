"""Shared launch/resume selection: explicit task > valid preference > default.

Resolves model, effort, permission and UI mode the same way for all three
runtimes. Interactive requests name their mediation explicitly; a bare TUI
never qualifies as container-mediated or all-writer fenced. Unknown
combinations fail closed instead of silently downgrading.
"""
from __future__ import annotations

import json
from pathlib import Path

UI_MODES = ('interactive', 'live-view', 'headless')
SOURCES = ('task', 'preference', 'default')


def _preferences(path):
    try:
        value = json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return {}
    if not isinstance(value, dict) or value.get('stale') is True:
        return {}
    return value


def _pick(name, task, preferences, default):
    if name in task and task[name] is not None:
        return task[name], 'task'
    if isinstance(preferences.get(name), str) and preferences[name]:
        return preferences[name], 'preference'
    return default, 'default'


def resolve_launch(task, preferences_path, runtime):
    """Resolve one launch/resume selection with provenance per field."""
    if runtime not in ('claude', 'codex', 'muse'):
        raise ValueError('unsupported runtime')
    task = dict(task or {})
    preferences = _preferences(preferences_path)
    defaults = {'model': 'native', 'effort': 'native', 'permission_mode': 'native',
                'ui_mode': 'headless'}
    resolved = {name: _pick(name, task, preferences, default)
                for name, default in defaults.items()}
    ui_mode, ui_source = resolved['ui_mode']
    if ui_mode not in UI_MODES:
        if ui_source == 'task':
            raise ValueError('unknown UI mode: ' + str(ui_mode))
        resolved['ui_mode'] = ('headless', 'default')
        ui_mode = 'headless'
    mediation = task.get('mediation', preferences.get('mediation', 'mediated'))
    if runtime == 'muse' and ui_mode == 'interactive' and mediation == 'none':
        raise ValueError('interactive bare-TUI Muse lacks container mediation and all-writer fence; select mediated headless/live-view or qualify the gap')
    resolved['mediation'] = (mediation, 'task' if 'mediation' in task else
                             ('preference' if 'mediation' in preferences else 'default'))
    return resolved


def check_resume_readback(selected, effective, expected_item=None, expected_native=None):
    """Verify a resumed native kept its selected native/task/claim identity.

    Returns the verified native session id. Any mismatch in model, effort,
    permission, UI mode, native identity or task item fails closed instead
    of silently adopting the drifted configuration.
    """
    for name in ('model', 'effort', 'permission_mode', 'ui_mode'):
        if effective.get(name) != selected.get(name):
            raise ValueError('resumed %s %r differs from selected %r; no silent repair'
                             % (name, effective.get(name), selected.get(name)))
    native = effective.get('native_session')
    if not native:
        raise ValueError('resumed native session identity is missing')
    if expected_native is not None and native != expected_native:
        raise ValueError('resumed native %r differs from bound %r'
                         % (native, expected_native))
    if effective.get('task_item') is not None and expected_item is not None \
            and effective['task_item'] != expected_item:
        raise ValueError('resumed task %r differs from assigned %r'
                         % (effective['task_item'], expected_item))
    return native


def verify_surface(tree, workspace_id, surface_ref):
    """Verify the new session surface is the visible selected one.

    Takes a parsed `cmux tree --all --json` document plus the owned
    workspace UUID and expected surface ref. Returns a surface claim for
    launch_receipt. A hidden surface, a surface in another workspace, or
    an unknown ref fails closed: the old shell or a log view never counts
    as interactive success. Only the caller's owned workspace is read.
    """
    if not isinstance(tree, dict):
        raise ValueError('cmux tree output is unavailable')
    for window in tree.get('windows', []) or []:
        for workspace in (window or {}).get('workspaces', []) or []:
            if (workspace or {}).get('id') != workspace_id:
                continue
            for pane in (workspace or {}).get('panes', []) or []:
                for surface in (pane or {}).get('surfaces', []) or []:
                    surface = surface or {}
                    if surface.get('ref') != surface_ref and surface.get('id') != surface_ref:
                        continue
                    if surface.get('active') is True and surface.get('selected') is True:
                        return {'visible': True, 'selected': True, 'surface_id': surface_ref,
                                'workspace_id': workspace_id}
                    raise ValueError('surface %s in workspace %s is hidden or unselected'
                                     % (surface_ref, workspace_id))
    raise ValueError('surface %s not found in owned workspace %s' % (surface_ref, workspace_id))


def launch_receipt(config_check, process, runtime, surface, task, outcome):
    """Build one unified startup receipt from separately verified stages.

    Config validation, process spawn, effective runtime, visible/selected
    surface, task/ownership and result path are distinct stages. An
    interactive request on a hidden surface, or a selected surface owned by
    an old shell PID, is blocked: neither proves the user can see or
    interact with the new session.
    """
    receipt = {'config': (config_check or {}).get('status', 'unknown'),
               'process': (process or {}).get('pid'),
               'runtime': '%s:%s' % ((runtime or {}).get('runtime', '?'),
                                     (runtime or {}).get('native_session', '?')),
               'surface': 'unknown',
               'task': '%s:%s:%s' % ((task or {}).get('item', '?'),
                                     (task or {}).get('claim', '?'),
                                     (task or {}).get('heartbeat', '?')),
               'report': (outcome or {}).get('report_path')}
    surface = surface or {}
    process = process or {}
    if surface.get('visible') is True and surface.get('selected') is True:
        shell_pid = surface.get('shell_pid')
        if shell_pid is not None and shell_pid != process.get('pid'):
            receipt.update(status='blocked', surface='stale-shell',
                           reason='selected surface is owned by an old shell, not the new session process')
        else:
            receipt.update(status='launched', surface='visible-selected')
    else:
        receipt.update(status='blocked', surface='hidden-or-unselected',
                       reason='new session surface is hidden or unselected; old shell or log view is not success')
    return receipt
