"""Initial migration: create request_logs and error_logs tables

Revision ID: 001
Revises:
Create Date: 2026-01-07

"""

from typing import Any, Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "001"
down_revision: str | None = None
branch_labels: Sequence[str] | None = None
depends_on: Sequence[str] | None = None


def upgrade() -> None:
    """Create initial tables."""
    # Create request_logs table
    op.create_table(
        "request_logs",
        sa.Column("id", postgresql.UUID(as_uuid=True), primary_key=True, default=sa.func.uuid_generate_v4()),
        sa.Column("request_time", sa.DateTime(), nullable=False, index=True),
        sa.Column("model_name", sa.String(255), nullable=False, index=True),
        sa.Column("is_stream", sa.Boolean(), nullable=False, default=False),
        sa.Column("path", sa.String(512), nullable=True),
        sa.Column("method", sa.String(10), nullable=True),
        sa.Column("query", sa.String(1024), nullable=True),
        sa.Column("headers", postgresql.JSON, nullable=True),
        sa.Column("body", postgresql.JSON, nullable=True),
        sa.Column("route", postgresql.JSON, nullable=True),
        sa.Column("backend_attempts", postgresql.JSON, nullable=True),
        sa.Column("stream_chunks", postgresql.JSON, nullable=True),
        sa.Column("errors", postgresql.JSON, nullable=True),
        sa.Column("usage_stats", postgresql.JSON, nullable=True),
        sa.Column("outcome", sa.String(50), nullable=True),
        sa.Column("duration_ms", sa.Integer(), nullable=True),
        sa.Column("request_path", sa.String(1024), nullable=True),
        sa.Column("created_at", sa.DateTime(), nullable=False, default=sa.func.now()),
        comment="Chat completion request and response logs",
    )

    # Create error_logs table
    op.create_table(
        "error_logs",
        sa.Column("id", postgresql.UUID(as_uuid=True), primary_key=True, default=sa.func.uuid_generate_v4()),
        sa.Column("timestamp", sa.DateTime(), nullable=False, index=True),
        sa.Column("model_name", sa.String(255), nullable=True, index=True),
        sa.Column("error_type", sa.String(100), nullable=False, index=True),
        sa.Column("error_message", sa.Text(), nullable=False),
        sa.Column("backend_name", sa.String(255), nullable=True),
        sa.Column("http_status", sa.Integer(), nullable=True),
        sa.Column("request_path", sa.String(1024), nullable=True),
        sa.Column("request_log_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column("extra_context", postgresql.JSON, nullable=True),
        sa.Column("created_at", sa.DateTime(), nullable=False, default=sa.func.now()),
        sa.ForeignKeyConstraint(
            ["request_log_id"],
            ["request_logs.id"],
            ondelete="SET NULL",
        ),
        comment="Error event logs with optional request reference",
    )

    # Create indexes for better query performance
    op.create_index("ix_request_logs_outcome", "request_logs", ["outcome"])
    op.create_index("ix_request_logs_created_at", "request_logs", ["created_at"])
    op.create_index("ix_error_logs_created_at", "error_logs", ["created_at"])


def downgrade() -> None:
    """Drop all tables."""
    op.drop_index("ix_error_logs_created_at", table_name="error_logs")
    op.drop_index("ix_request_logs_created_at", table_name="request_logs")
    op.drop_index("ix_request_logs_outcome", table_name="request_logs")
    op.drop_table("error_logs")
    op.drop_table("request_logs")
