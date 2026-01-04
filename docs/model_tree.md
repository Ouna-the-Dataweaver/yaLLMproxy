# Model Tree Structure for Dynamic Inheritance

This document describes a proposed feature to maintain a tree structure for model inheritance, enabling dynamic inheritance propagation, cascading deletes, and improved administrative capabilities.

## Overview

Currently, model inheritance is resolved statically at config load time. Changes to base models do not automatically propagate to derived models, and deleting a base model can leave derived models in an inconsistent state. This proposal introduces a persistent tree structure that maintains the full inheritance hierarchy in memory, enabling dynamic behavior and better administrative visibility.

## Problem Statement

### Current Limitations

The existing inheritance system resolves model inheritance once during configuration loading:

```yaml
# config_default.yaml
- model_name: base-model
  model_params:
    api_base: https://api.example.com/v1
    timeout: 120

- model_name: derived-model
  extends: base-model
```

When `base-model` is updated, `derived-model` retains the old values because inheritance was flattened at load time. This creates several operational challenges:

**1. Manual propagation required**: When modifying a base model, all derived models must be updated manually. This is error-prone and doesn't scale with complex inheritance hierarchies, and automatic full reload is inefficient.

**2. No visibility into inheritance**: The admin UI and APIs provide no way to see which models inherit from which. Users cannot easily understand their model topology.

**3. Dangerous deletes**: Deleting a base model leaves derived models with dangling references. The system has no way to warn users or automatically handle this situation.

**4. Inconsistent state after reload**: Config reloads re-resolve inheritance from scratch, potentially creating inconsistencies with runtime modifications made through the API.

### Impact on Operations

These limitations affect day-to-day operations in several ways:

- **Configuration management**: Teams must maintain careful documentation of inheritance relationships, often in external systems.

- **Change management**: Modifying base configurations requires auditing all derived models, increasing the risk of missed updates.

- **Debugging**: When derived models don't behave as expected, it's difficult to trace whether the issue stems from inheritance resolution.

- **User experience**: New users struggle to understand why their derived models don't reflect changes to parent models.

## Proposed Solution

### Core Idea

Maintain a tree structure that persists the full inheritance hierarchy alongside the existing flat storage. This tree serves as the source of truth for inheritance relationships, enabling dynamic resolution and administrative features.

```
                    [base-model]
                         │
                         ├── [derived-model-1]
                         │        │
                         │        └── [deep-derived-1]
                         │
                         └── [derived-model-2]
```

The router continues to use flat `Backend` objects for performance, but the tree structure provides:

- Dynamic inheritance resolution
- Cascading delete with warnings
- Inheritance chain queries for admin APIs
- Visual representation for UI

### Architecture

The solution introduces a new `ModelTree` class that maintains the inheritance hierarchy:

```python
class ModelNode:
    """A node in the model inheritance tree."""
    name: str                              # Model name (unique identifier)
    config: dict[str, Any]                 # This model's direct configuration
    parent: Optional[str]                  # Parent model name or None for roots
    children: list[str]                   # List of child model names
    source: str                            # "default" or "added"
    editable: bool                        # Can be modified at runtime
    inheritance_chain: list[str]          # Full ancestry: [self, parent, grandparent, ...]
    inherited_fields: dict[str, Any]      # Fields inherited from ancestors
    own_fields: dict[str, Any]            # Fields defined directly on this model
```

The `ModelTree` class provides:

```python
class ModelTree:
    """Maintains the model inheritance tree."""
    
    def __init__(self) -> None:
        self.nodes: dict[str, ModelNode] = {}
        self.roots: list[str] = []
    
    def build(self, models: list[dict[str, Any]]) -> None:
        """Build tree from flat model list with resolved inheritance."""
    
    def get_node(self, model_name: str) -> Optional[ModelNode]:
        """Retrieve a node by model name."""
    
    def get_children(self, model_name: str) -> list[str]:
        """Get all direct children of a model."""
    
    def get_descendants(self, model_name: str) -> list[str]:
        """Get all descendants (recursive) of a model."""
    
    def get_ancestors(self, model_name: str) -> list[str]:
        """Get all ancestors (recursive) of a model."""
    
    def has_ancestor(self, model_name: str, ancestor_name: str) -> bool:
        """Check if ancestor_name is in model_name's ancestry."""
    
    def get_inheritance_chain(self, model_name: str) -> list[str]:
        """Get the full inheritance chain for a model."""
    
    def delete_model(self, model_name: str, cascade: bool = False) -> DeleteResult:
        """Delete a model, optionally cascading to descendants."""
```

### Storage Integration

The `ModelTree` integrates with the existing `ConfigStore` without disrupting current behavior:

```python
class ConfigStore:
    def __init__(self, ...) -> None:
        # Existing storage
        self._default_raw: dict[str, Any] = {}
        self._added_raw: dict[str, Any] = {}
        
        # New tree storage
        self._model_tree: ModelTree = ModelTree()
    
    def reload(self) -> None:
        # Existing behavior: reload raw configs
        self._default_raw = load_config(...)
        self._added_raw = load_config(...)
        
        # New behavior: rebuild tree
        self._model_tree = ModelTree()
        self._model_tree.build(self.get_runtime_config()["model_list"])
    
    def list_models(self, resolve_inheritance: bool = True) -> tuple[...]:
        # Existing behavior unchanged
        ...
    
    def get_model_tree(self) -> ModelTree:
        """Get the model tree for administrative operations."""
        return self._model_tree
    
    def find_model(self, model_name: str) -> dict[str, Any] | None:
        # Existing behavior unchanged
        ...
```

### API Extensions

The tree structure enables new API endpoints for administrative operations:

```python
# GET /admin/models/tree - Get full model tree
{
    "roots": ["base-model"],
    "nodes": {
        "base-model": {
            "config": {...},
            "parent": null,
            "children": ["derived-model-1", "derived-model-2"],
            "source": "default",
            "editable": false
        },
        "derived-model-1": {
            "config": {...},
            "parent": "base-model",
            "children": ["deep-derived-1"],
            "source": "added",
            "editable": true
        }
    }
}

# GET /admin/models/{name}/ancestry - Get inheritance chain
{
    "model": "deep-derived-1",
    "chain": ["deep-derived-1", "derived-model-1", "base-model"],
    "inheritance_depth": 3
}

# GET /admin/models/{name}/dependents - Get models that depend on this one
{
    "model": "base-model",
    "direct_children": ["derived-model-1", "derived-model-2"],
    "all_descendants": ["derived-model-1", "derived-model-2", "deep-derived-1"],
    "descendant_count": 3
}

# DELETE /admin/models/{name} - Delete with cascade control
{
    "cascade": false,  # Default: fail if children exist
    "force": false     # If true, also delete descendants
}

# Response when deletion would orphan children:
{
    "error": "Cannot delete model with existing dependents",
    "dependents": ["derived-model-1", "derived-model-2"],
    "hint": "Set cascade=true to delete dependents, or update them to use a different parent"
}
```

### Dynamic Inheritance

With the tree structure, updates to base models can propagate to derived models:

```python
# Update a base model
await store.update_model("base-model", {"timeout": 300})

# Tree automatically recomputes inherited_fields for all descendants
derived_model = store.find_model("derived-model-1")
# derived_model now reflects timeout: 300 from base-model
```

This requires:

1. **Recomputation on update**: When a model is modified, the tree recomputes inherited fields for all descendants.

2. **Change detection**: Only propagate changes that actually affect inherited values.

3. **Event system**: Notify the router when dependent models change, enabling hot-reload of affected backends.

## Implementation Approaches

### Approach 1: Pure In-Memory Tree

The simplest approach maintains the tree entirely in memory, rebuilding it on config reload and recomputing inherited fields on updates.

**Pros:**
- Minimal complexity, no new dependencies
- Fast read operations (O(1) for most queries)
- No additional storage requirements

**Cons:**
- Tree state lost on restart (though configs are on disk)
- Requires full rebuild on reload, which could be slow for large configs
- No persistence of tree-specific metadata (like manual inheritance overrides)

**Complexity:** Low

### Approach 2: Persistent Tree with Metadata

Extend the YAML config format to include tree metadata:

```yaml
# config_added.yaml
model_list:
  - model_name: derived-model
    extends: base-model
    _inheritance_metadata:
      resolved_parent: base-model
      inheritance_chain: [base-model, derived-model]
      inherited_fields: {timeout: 120, api_base: https://api.example.com/v1}
    model_params:
      temperature: 0.7  # Only this is stored as "own_fields"
```

**Pros:**
- Survives restarts without rebuild
- Enables faster loading (no recomputation needed)
- Can store additional metadata

**Cons:**
- Modifies config file format
- Risk of metadata drift from actual inheritance
- More complex merge logic

**Complexity:** Medium

### Approach 3: Separate Tree Storage

Store the tree in a separate file or database (SQLite, etc.):

```python
# tree_store.json
{
    "version": "1",
    "models": {
        "base-model": {
            "config": {...},
            "parent": null,
            "children": ["derived-model-1"],
            "inherited_fields": {},
            "own_fields": {...}
        }
    },
    "metadata": {
        "last_modified": "2026-01-04T12:00:00Z",
        "config_versions": {...}
    }
}
```

**Pros:**
- Clean separation of concerns
- Enables advanced features (search, queries)
- Can track historical changes
- No config file modification

**Cons:**
- Additional dependency (if using a database)
- More complex deployment
- Risk of tree/config desync
- Overhead for small deployments

**Complexity:** High

### Recommendation

Start with **Approach 1 (Pure In-Memory Tree)**:

1. Minimally invasive to existing code
2. Fast to implement and test
3. Provides immediate value for inheritance visibility
4. Can be enhanced later with persistence if needed

The in-memory tree can be extended with persistence as a future enhancement.

## Key Design Decisions

### 1. Tree Structure Design

**Decision:** Store nodes by name, with parent and children references.

```python
nodes: dict[str, ModelNode]  # O(1) lookup by name
roots: list[str]             # Fast enumeration of root nodes
```

**Rationale:** Most operations are "find model by name" or "get all children of model". The dictionary structure supports these efficiently.

### 2. Inheritance Resolution

**Decision:** Compute inherited fields dynamically on query, not on every change.

```python
class ModelNode:
    @property
    def inherited_fields(self) -> dict[str, Any]:
        if self._cached_inherited is None:
            self._cached_inherited = self._compute_inherited()
        return self._cached_inherited
```

**Rationale:** Computing inheritance is expensive (traversing the full chain). Cache the result and invalidate when ancestors change.

### 3. Change Propagation

**Decision:** On model update, mark descendants as "dirty" and recompute on next access.

```python
def update_model(self, name: str, new_config: dict[str, Any]) -> None:
    self.nodes[name].config = new_config
    for descendant in self.get_descendants(name):
        self.nodes[descendant]._cached_inherited = None
```

**Rationale:** Lazy recomputation avoids cascading updates during bulk operations. The router can trigger explicit recomputation when needed.

### 4. Delete Semantics

**Decision:** Default to failing deletes that would orphan children. Support explicit cascade.

```python
def delete_model(self, name: str, cascade: bool = False) -> DeleteResult:
    children = self.get_children(name)
    if children and not cascade:
        return DeleteResult(
            success=False,
            error="Cannot delete model with dependents",
            dependents=children
        )
    # ... proceed with deletion
```

**Rationale:** Prevents accidental data loss. Users must explicitly opt into cascading deletes.

### 5. Router Independence

**Decision:** The router continues to use flat `Backend` objects. The tree is an additional layer for administrative operations.

```python
# Router uses flat structure (unchanged)
self.backends: Dict[str, Backend]  # For routing

# Tree is for admin/UI only
store.get_model_tree()  # Returns tree structure
```

**Rationale:** Performance is critical for routing. The tree structure adds some overhead that shouldn't affect request handling.

## Things to Keep an Eye On

### Performance Considerations

1. **Tree rebuild time**: For large configs (100+ models), rebuilding the tree on every reload may cause latency spikes. Consider incremental updates.

2. **Memory usage**: The tree doubles the in-memory footprint for model storage. Monitor for memory pressure in constrained environments.

3. **Query complexity**: Some queries (e.g., "find all models with property X in inheritance chain") could be expensive. Document complexity expectations.

### Consistency Guarantees

1. **Tree/config sync**: After manual edits to YAML files, the tree must be rebuilt. Ensure the reload API triggers this.

2. **API updates**: When models are modified via API, the tree must be updated atomically with the config store.

3. **Partial failures**: If tree update fails, ensure the config store remains consistent.

### Edge Cases

1. **Circular references**: The existing inheritance resolver prevents infinite loops. The tree must preserve this protection.

2. **Missing parents**: Handle gracefully if a model's parent is deleted without cascade.

3. **Deep inheritance chains**: Limit recursion depth to prevent stack overflow.

4. **Type mismatches**: When inheriting fields, handle type coercion carefully.

### Testing Requirements

1. **Unit tests**: Test tree building, queries, updates, and deletes.

2. **Integration tests**: Test tree behavior with config reload, API updates.

3. **Performance tests**: Measure tree operations with large model counts.

4. **Edge case tests**: Circular references, deep chains, missing parents.

### Migration Considerations

1. **Backward compatibility**: Existing configs must work without modification.

2. **API stability**: New endpoints should be clearly marked as admin/admin-only.

3. **Default behavior**: Existing delete operations should fail with a clear error if they would orphan children.

## Implementation Roadmap

### Phase 1: Tree Structure (MVP)

- [ ] Define `ModelNode` and `ModelTree` classes
- [ ] Implement tree building from flat model list
- [ ] Add basic queries: `get_node`, `get_children`, `get_ancestors`
- [ ] Integrate tree building into `ConfigStore.reload()`
- [ ] Add `get_model_tree()` method to `ConfigStore`
- [ ] Add admin API endpoint `GET /admin/models/tree`

### Phase 2: Dynamic Updates

- [ ] Implement tree updates on model modifications
- [ ] Add change propagation to descendants
- [ ] Add `GET /admin/models/{name}/ancestry` endpoint
- [ ] Add `GET /admin/models/{name}/dependents` endpoint
- [ ] Update admin UI to show inheritance

### Phase 3: Safe Deletes

- [ ] Implement delete with dependency checking
- [ ] Add `DELETE /admin/models/{name}` with cascade support
- [ ] Add conflict detection and helpful error messages
- [ ] Add `cascade` option to API

### Phase 4: Advanced Features

- [ ] Lazy recomputation with caching
- [ ] Event system for router notifications
- [ ] Tree persistence (optional)
- [ ] Advanced queries (search, filter by inheritance)

## Related Files

- `src/config_store.py` - Main configuration storage
- `src/core/router.py` - Router using flat backend structure
- `src/core/backend.py` - Backend dataclass definition
- `src/api/routes/config.py` - Admin API endpoints
- `static/admin/` - Admin UI files

## References

- Issue tracking: GitHub issue for dynamic inheritance
- Tests: `tests/test_model_inheritance.py`
- Related: Hot-reload feature (`POST /admin/config/reload`)
