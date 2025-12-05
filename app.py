import os
import subprocess
from flask import Flask, jsonify, request, send_from_directory
from flask_cors import CORS
from dotenv import load_dotenv
from functools import wraps

# Load environment variables from .env file
load_dotenv()

app = Flask(__name__, static_folder='frontend/build', static_url_path='')
CORS(app)

def require_tailscale_ip(f):
    """Decorator to check if request is from Tailscale network"""
    @wraps(f)
    def decorated_function(*args, **kwargs):
        client_ip = request.remote_addr
        
        # Check if IP is in Tailscale range (100.64.0.0/10)
        if client_ip.startswith('100.'):
            return f(*args, **kwargs)
        
        # Allow localhost for testing
        if client_ip in ['127.0.0.1', 'localhost', '::1']:
            return f(*args, **kwargs)
        
        return jsonify({
            'status': 'error',
            'message': 'Access denied. Only Tailscale network access allowed.'
        }), 403
    
    return decorated_function

# Serve React app
@app.route('/')
def serve_react():
    return send_from_directory(app.static_folder, 'index.html')

@app.route('/<path:path>')
def serve_static(path):
    if path != "" and os.path.exists(os.path.join(app.static_folder, path)):
        return send_from_directory(app.static_folder, path)
    else:
        return send_from_directory(app.static_folder, 'index.html')

@app.route('/api/hello', methods=['GET'])
def hello():
    """Simple GET endpoint that returns a greeting message"""
    return jsonify({
        'message': 'Hello, World!',
        'status': 'success'
    })

@app.route('/api/docker/up/<container_name>', methods=['GET'])
def docker_up(container_name):
    """Run docker compose up -d using the configured docker-compose file for the specified container"""
    try:
        # Get docker-compose file path from environment variable based on container name
        env_var_name = f'DOCKER_COMPOSE_PATH_{container_name.upper()}'
        docker_compose_path = os.getenv(env_var_name)
        
        if not docker_compose_path:
            return jsonify({
                'status': 'error',
                'message': f'Environment variable {env_var_name} not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(docker_compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {docker_compose_path}'
            }), 404
        
        # Run docker compose up -d
        result = subprocess.run(
            ['docker-compose', '-f', docker_compose_path, 'up', '-d'],
            capture_output=True,
            text=True,
            timeout=300  # 5 minute timeout
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'container': container_name,
                'message': 'Docker compose up completed successfully',
                'output': result.stdout
            })
        else:
            return jsonify({
                'status': 'error',
                'container': container_name,
                'message': 'Docker compose up failed',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': 'Docker compose up timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': str(e)
        }), 500

@app.route('/api/docker/down/<container_name>', methods=['GET'])
def docker_down(container_name):
    """Run docker compose down using the configured docker-compose file for the specified container"""
    try:
        # Get docker-compose file path from environment variable based on container name
        env_var_name = f'DOCKER_COMPOSE_PATH_{container_name.upper()}'
        docker_compose_path = os.getenv(env_var_name)
        
        if not docker_compose_path:
            return jsonify({
                'status': 'error',
                'message': f'Environment variable {env_var_name} not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(docker_compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {docker_compose_path}'
            }), 404
        
        # Run docker compose down
        result = subprocess.run(
            ['docker-compose', '-f', docker_compose_path, 'down'],
            capture_output=True,
            text=True,
            timeout=300  # 5 minute timeout
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'container': container_name,
                'message': 'Docker compose down completed successfully',
                'output': result.stdout
            })
        else:
            return jsonify({
                'status': 'error',
                'container': container_name,
                'message': 'Docker compose down failed',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': 'Docker compose down timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': str(e)
        }), 500

@app.route('/api/docker/status/<container_name>', methods=['GET'])
def docker_status(container_name):
    """Get the status of containers defined in the docker-compose file"""
    try:
        # Get docker-compose file path from environment variable based on container name
        env_var_name = f'DOCKER_COMPOSE_PATH_{container_name.upper()}'
        docker_compose_path = os.getenv(env_var_name)
        
        if not docker_compose_path:
            return jsonify({
                'status': 'error',
                'message': f'Environment variable {env_var_name} not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(docker_compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {docker_compose_path}'
            }), 404
        
        # Run docker compose ps to get container status
        result = subprocess.run(
            ['docker-compose', '-f', docker_compose_path, 'ps', '--all', '--format', 'json'],
            capture_output=True,
            text=True,
            timeout=30
        )
        
        if result.returncode == 0:
            # Parse JSON output
            import json
            try:
                containers = []
                if result.stdout.strip():
                    # Docker compose ps can return multiple JSON objects, one per line
                    for line in result.stdout.strip().split('\n'):
                        if line:
                            container_data = json.loads(line)
                            # Extract only the fields we want
                            containers.append({
                                'Service': container_data.get('Service', ''),
                                'Size': container_data.get('Size', '0B'),
                                'State': container_data.get('State', ''),
                                'Status': container_data.get('Status', '')
                            })
                
                return jsonify({
                    'status': 'success',
                    'container': container_name,
                    'containers': containers,
                    'total': len(containers)
                })
            except json.JSONDecodeError:
                return jsonify({
                    'status': 'success',
                    'container': container_name,
                    'message': 'No containers running',
                    'containers': []
                })
        else:
            return jsonify({
                'status': 'error',
                'container': container_name,
                'message': 'Failed to get container status',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': 'Docker status check timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'container': container_name,
            'message': str(e)
        }), 500

if __name__ == '__main__':
    app.run(debug=True, host='0.0.0.0', port=5000)
