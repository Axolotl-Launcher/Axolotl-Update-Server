from flask import jsonify, request


class ApiError(Exception):
    def __init__(self, code: str, message: str, status: int = 400):
        self.code, self.message, self.status = code, message, status


def error_response(error: ApiError):
    return jsonify({"error": {"code": error.code, "message": error.message, "request_id": request.environ.get("request_id", "")}}), error.status


def register_error_handlers(app):
    @app.errorhandler(ApiError)
    def handle_api_error(error):
        return error_response(error)

    @app.errorhandler(413)
    def handle_too_large(_error):
        return error_response(ApiError("payload_too_large", "Request body exceeds the configured limit.", 413))
