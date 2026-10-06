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
