import { Copy, Pencil, RefreshCw } from "lucide-react";
import type { AppKeyRecord, JsonRecord } from "../types";
import { DeleteButton } from "./common";
import { Button } from "./ui/button";

export function ModelTreeActions({
  name,
  disabled,
  onCopy,
  onDelete
}: {
  name?: string;
  disabled: boolean;
  onCopy: (name?: string) => void;
  onDelete: (name?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !name} onClick={() => onCopy(name)} title={name ? `Copy ${name}` : "Copy"}>
        <Copy size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={name} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}

export function AppKeyActions({
  row,
  keyID,
  disabled,
  onEdit,
  onRegenerate,
  onDelete
}: {
  row: AppKeyRecord;
  keyID?: string;
  disabled: boolean;
  onEdit: (row: AppKeyRecord) => void;
  onRegenerate: (keyID?: string) => Promise<void>;
  onDelete: (keyID?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !keyID} onClick={() => onEdit(row)} title={keyID ? `Edit ${keyID}` : "Edit"}>
        <Pencil size={16} aria-hidden="true" />
      </Button>
      <Button variant="secondary" size="icon" disabled={disabled || !keyID} onClick={() => void onRegenerate(keyID)} title={keyID ? `Regenerate ${keyID}` : "Regenerate"}>
        <RefreshCw size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={keyID} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}

export function RoutingActions<T extends JsonRecord>({
  row,
  name,
  disabled,
  onEdit,
  onDelete
}: {
  row: T;
  name?: string;
  disabled: boolean;
  onEdit: (row: T) => void;
  onDelete: (name?: string) => Promise<void>;
}) {
  return (
    <span className="action-cell">
      <Button variant="secondary" size="icon" disabled={disabled || !name} onClick={() => onEdit(row)} title={name ? `Edit ${name}` : "Edit"}>
        <Pencil size={16} aria-hidden="true" />
      </Button>
      <DeleteButton name={name} disabled={disabled} onDelete={onDelete} />
    </span>
  );
}
