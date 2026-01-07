"""Enhance request_logs table with stop_reason, full_response, and agentic workflow fields

Revision ID: 002
Revises: 001
Create Date: 2026-01-07

"""

from typing import Any, Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "002"
down_revision: str | None = "001"
branch_labels: Sequence[str] | None = None
depends_on: Sequence[str] | None = None


def upgrade() -> None:
    """Add new columns for enhanced logging."""
    # Add stop_reason column
    op.add_column(
        "request_logs",
        sa.Column(
            "stop_reason",
            sa.String(50),
            nullable=True,
            comment="Finish reason from the response: stop, tool_calls, length, content_filter, etc."
        )
    )
    op.create_index("ix_request_logs_stop_reason", "request_logs", ["stop_reason"])

    # Add full_response column for concatenated streaming responses
    op.add_column(
        "request_logs",
        sa.Column(
            "full_response",
            sa.Text,
            nullable=True,
            comment="Concatenated complete response text (especially for streaming)"
        )
    )

    # Add is_tool_call column to track requests that resulted in tool calls
    op.add_column(
        "request_logs",
        sa.Column(
            "is_tool_call",
            sa.Boolean,
            nullable=False,
            default=False,
            comment="Whether this request resulted in tool/function calls"
        )
    )

    # Add conversation_turn column for agentic workflow tracking
    op.add_column(
        "request_logs",
        sa.Column(
            "conversation_turn",
            sa.Integer,
            nullable=True,
            comment="Turn number in agentic conversation sequence"
        )
    )
    op.create_index("ix_request_logs_conversation_turn", "request_logs", ["conversation_turn"])


def downgrade() -> None:
    """Remove the added columns."""
    # Drop indexes first
    op.drop_index("ix_request_logs_conversation_turn", table_name="request_logs")
    op.drop_index("ix_request_logs_stop_reason", table_name="request_logs")

    # Drop columns
    op.drop_column("request_logs", "conversation_turn")
    op.drop_column("request_logs", "is_tool_call")
    op.drop_column("request_logs", "full_response")
    op.drop_column("request_logs", "stop_reason")
