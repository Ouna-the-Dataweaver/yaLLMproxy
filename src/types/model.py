"""types for the models and related things"""

class Model:
    """a model from fixed provider"""
    model_name: str
    context_length: int
    
    provider: str | None