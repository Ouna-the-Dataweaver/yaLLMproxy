"""Logging configuration for the proxy."""

import logging
import sys


def setup_logging() -> logging.Logger:
    """Set up logging with proper handlers and formatters."""
    logger = logging.getLogger("yallmp-proxy")
    logger.setLevel(logging.INFO)
    
    # Clear any existing handlers
    logger.handlers.clear()
    
    # Create console handler with proper formatting
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(logging.INFO)
    
    # Create formatter with timestamp and level
    formatter = logging.Formatter(
        '%(asctime)s - %(name)s - %(levelname)s - %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )
    console_handler.setFormatter(formatter)
    
    # Add handler to logger
    logger.addHandler(console_handler)
    
    # Set logger to propagate to root logger to ensure proper flushing
    logger.propagate = True
    
    return logger


# Global logger instance
logger = setup_logging()

