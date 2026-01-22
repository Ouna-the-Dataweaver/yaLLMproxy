"""Logging configuration for the proxy."""

import logging
import sys
from pathlib import Path

# Log file paths (relative to project root)
CONSOLE_LOG_PATH = "logs/console.log"
TCP_FORWARDER_LOG_PATH = "logs/console_forwarder.log"
HTTP_FORWARDER_LOG_PATH = "logs/console_http_forwarder.log"

# Standard log format
LOG_FORMAT = '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
LOG_DATE_FORMAT = '%Y-%m-%d %H:%M:%S'


def setup_logging(
    debug: bool = False,
    log_file: str | None = None,
    logger_name: str = "yallmp-proxy",
) -> logging.Logger:
    """Set up logging with proper handlers and formatters.

    Args:
        debug: If True, set level to DEBUG and optionally write to log_file.
        log_file: Path to console log file (overwritten on restart).
                  Only used when debug=True.
        logger_name: Name for the logger instance.

    Returns:
        Configured logger instance.
    """
    logger = logging.getLogger(logger_name)
    log_level = logging.DEBUG if debug else logging.INFO
    logger.setLevel(log_level)

    # Clear any existing handlers
    logger.handlers.clear()

    # Create formatter with timestamp and level
    formatter = logging.Formatter(LOG_FORMAT, datefmt=LOG_DATE_FORMAT)

    # Create console handler with proper formatting
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(log_level)
    console_handler.setFormatter(formatter)
    logger.addHandler(console_handler)

    # Add file handler if debug mode with log file specified
    if debug and log_file:
        log_path = Path(log_file)
        log_path.parent.mkdir(parents=True, exist_ok=True)
        # mode='w' overwrites on restart
        file_handler = logging.FileHandler(log_path, mode='w', encoding='utf-8')
        file_handler.setLevel(logging.DEBUG)
        file_handler.setFormatter(formatter)
        logger.addHandler(file_handler)

    # Set logger to propagate to root logger to ensure proper flushing
    logger.propagate = True

    return logger


def reconfigure_logging(
    debug: bool,
    log_file: str | None = None,
    logger_name: str = "yallmp-proxy",
) -> logging.Logger:
    """Reconfigure an existing logger with debug settings.

    This is called after config is loaded to enable debug mode if configured.

    Args:
        debug: If True, set level to DEBUG and add file handler.
        log_file: Path to console log file (overwritten on restart).
        logger_name: Name of the logger to reconfigure.

    Returns:
        Reconfigured logger instance.
    """
    logger = logging.getLogger(logger_name)

    if not debug:
        # No changes needed if debug is not enabled
        return logger

    log_level = logging.DEBUG
    logger.setLevel(log_level)

    # Update existing handlers to DEBUG level
    for handler in logger.handlers:
        handler.setLevel(log_level)

    # Add file handler if specified and not already present
    if log_file:
        # Check if file handler already exists
        has_file_handler = any(
            isinstance(h, logging.FileHandler) for h in logger.handlers
        )
        if not has_file_handler:
            formatter = logging.Formatter(LOG_FORMAT, datefmt=LOG_DATE_FORMAT)
            log_path = Path(log_file)
            log_path.parent.mkdir(parents=True, exist_ok=True)
            file_handler = logging.FileHandler(log_path, mode='w', encoding='utf-8')
            file_handler.setLevel(logging.DEBUG)
            file_handler.setFormatter(formatter)
            logger.addHandler(file_handler)

    logger.debug("Debug logging enabled")
    return logger


def setup_forwarder_logging(
    logger_name: str,
    debug: bool = False,
    log_file: str | None = None,
) -> logging.Logger:
    """Set up logging for standalone forwarder scripts.

    Similar to setup_logging but designed for forwarders that run independently.

    Args:
        logger_name: Name for the logger instance.
        debug: If True, set level to DEBUG and write to log_file.
        log_file: Path to console log file (overwritten on restart).

    Returns:
        Configured logger instance.
    """
    return setup_logging(
        debug=debug,
        log_file=log_file if debug else None,
        logger_name=logger_name,
    )


# Global logger instance (initially INFO level, can be reconfigured later)
logger = setup_logging()

