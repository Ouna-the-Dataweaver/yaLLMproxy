#!/usr/bin/env python
"""Database cleanup script for removing request/error logs.

Usage:
    python scripts/db_clean.py [--keep-days N] [--requests] [--errors] [--all]
"""

import argparse
import sys
from pathlib import Path
from datetime import datetime, timedelta

# Set up database path
DB_PATH = Path(__file__).parent.parent / "logs" / "yaLLM.db"
DATABASE_URL = f"sqlite:///{DB_PATH}"


def get_table_classes():
    """Import and return database table classes."""
    # Import SQLAlchemy components
    from sqlalchemy import create_engine, delete, func, Column, DateTime, String, JSON, Integer, UUID
    from sqlalchemy.orm import sessionmaker, declarative_base

    Base = declarative_base()

    class RequestLog(Base):
        """Request log model."""
        __tablename__ = "request_logs"

        id = Column(UUID, primary_key=True)
        request_time = Column(DateTime, nullable=False)
        model_name = Column(String, nullable=False)
        is_stream = Column(Integer, nullable=False)
        path = Column(String, nullable=False)
        method = Column(String, nullable=False)
        query = Column(String, nullable=False)
        headers = Column(JSON)
        body = Column(JSON)
        route = Column(JSON)
        backend_attempts = Column(JSON)
        stream_chunks = Column(JSON)
        errors = Column(JSON)
        usage_stats = Column(JSON)
        outcome = Column(String)
        duration_ms = Column(Integer)

    class ErrorLog(Base):
        """Error log model."""
        __tablename__ = "error_logs"

        id = Column(UUID, primary_key=True)
        timestamp = Column(DateTime, nullable=False)
        model_name = Column(String, nullable=False)
        error_type = Column(String, nullable=False)
        error_message = Column(String, nullable=False)
        backend_name = Column(String)
        http_status = Column(Integer)
        request_path = Column(String)
        request_log_id = Column(UUID)
        extra_context = Column(JSON)

    return RequestLog, ErrorLog


def get_database_engine():
    """Create and return database engine."""
    from sqlalchemy import create_engine
    engine = create_engine(DATABASE_URL, echo=False)
    return engine


def get_session():
    """Get a database session."""
    from sqlalchemy.orm import sessionmaker
    engine = get_database_engine()
    Session = sessionmaker(bind=engine)
    return Session()


def parse_args():
    parser = argparse.ArgumentParser(
        description="Clean request/error logs from the database",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
    Clean all request logs:
        python scripts/db_clean.py --requests

    Clean all error logs:
        python scripts/db_clean.py --errors

    Clean both requests and errors:
        python scripts/db_clean.py --all

    Clean logs older than 7 days:
        python scripts/db_clean.py --all --keep-days 7

    Clean logs older than 30 days (dry run):
        python scripts/db_clean.py --all --keep-days 30 --dry-run
        """,
    )

    parser.add_argument(
        "--requests", action="store_true", help="Clean request logs"
    )
    parser.add_argument(
        "--errors", action="store_true", help="Clean error logs"
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help="Clean both request and error logs (default if no specific type specified)",
    )
    parser.add_argument(
        "--keep-days",
        type=int,
        default=0,
        help="Only clean logs older than N days (0 = clean all)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be deleted without actually deleting",
    )

    return parser.parse_args()


def get_table_class(table_name: str):
    """Get SQLAlchemy table class by name."""
    RequestLog, ErrorLog = get_table_classes()

    tables = {
        "requests": RequestLog,
        "errors": ErrorLog,
    }
    return tables.get(table_name)


def clean_database(args):
    """Clean the database based on provided arguments."""
    # Determine what to clean
    clean_requests = args.requests or args.all
    clean_errors = args.errors or args.all

    # Default to both if nothing specified
    if not clean_requests and not clean_errors:
        clean_requests = True
        clean_errors = True

    # Check if database exists
    if not DB_PATH.exists():
        print(f"Database not found at {DB_PATH}")
        print("No cleanup needed")
        return

    session = get_session()
    try:
        if clean_requests:
            count = clean_table_logs(session, "requests", args.keep_days, args.dry_run)
            print(f"Request logs to clean: {count}")

        if clean_errors:
            count = clean_table_logs(session, "errors", args.keep_days, args.dry_run)
            print(f"Error logs to clean: {count}")

        if not args.dry_run:
            session.commit()
            print("Database cleaned successfully")
        else:
            print("Dry run completed - no changes made")
    finally:
        session.close()


def clean_table_logs(session, table_name: str, keep_days: int, dry_run: bool) -> int:
    """Clean logs from a specific table."""
    from sqlalchemy import func, delete
    table_class = get_table_class(table_name)
    if table_class is None:
        print(f"  Unknown table: {table_name}")
        return 0

    query = session.query(func.count()).select_from(table_class)

    # Get the timestamp field name based on table type
    timestamp_field = "request_time" if table_name == "requests" else "timestamp"

    if keep_days > 0:
        cutoff_date = datetime.utcnow() - timedelta(days=keep_days)
        query = query.filter(getattr(table_class, timestamp_field) < cutoff_date)

    count = query.scalar()

    if count == 0:
        print(f"  No {table_name} logs to clean")
        return 0

    print(f"  Found {count} {table_name} logs to clean")

    if not dry_run:
        if keep_days > 0:
            cutoff_date = datetime.utcnow() - timedelta(days=keep_days)
            session.execute(
                delete(table_class).where(
                    getattr(table_class, timestamp_field) < cutoff_date
                )
            )
        else:
            session.execute(delete(table_class))

    return count


def main():
    args = parse_args()

    print(f"Database cleanup starting...")
    if args.dry_run:
        print("DRY RUN - No changes will be made")
    print()

    try:
        clean_database(args)
    except Exception as e:
        print(f"Error cleaning database: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()
