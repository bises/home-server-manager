from flask import Flask, jsonify, send_from_directory
from flask_cors import CORS
from dotenv import load_dotenv
import os
import yaml
import subprocess
import json

# Load environment variables
load_dotenv()

app = Flask(__name__, static_folder='frontend/build', static_url_path='')
CORS(app)

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

@app.route('/api/services', methods=['GET'])
def get_services():
    """Get all services from the docker-compose file"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Read and parse the docker-compose file
        with open(compose_path, 'r') as file:
            compose_data = yaml.safe_load(file)
        
        # Extract service names
        services = list(compose_data.get('services', {}).keys())
        
        return jsonify({
            'status': 'success',
            'services': services,
            'total': len(services),
            'compose_file': compose_path
        })
        
    except yaml.YAMLError as e:
        return jsonify({
            'status': 'error',
            'message': f'Error parsing docker-compose file: {str(e)}'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'message': str(e)
        }), 500

@app.route('/api/containers/status', methods=['GET'])
def get_containers():
    """Get all running containers from docker-compose ps"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose ps -a --format json with explicit project name
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'ps', '-a', '--format', 'json'],
            capture_output=True,
            text=True,
            timeout=30
        )
        
        if result.returncode == 0:
            containers = []
            if result.stdout.strip():
                # Parse JSON output (one JSON object per line)
                for line in result.stdout.strip().split('\n'):
                    if line:
                        container_data = json.loads(line)
                        containers.append({
                            'Name': container_data.get('Name', ''),
                            'Service': container_data.get('Service', ''),
                            'State': container_data.get('State', ''),
                            'Status': container_data.get('Status', ''),
                            'Ports': container_data.get('Publishers', [])
                        })
            
            return jsonify({
                'status': 'success',
                'containers': containers,
                'total': len(containers),
                'compose_file': compose_path
            })
        else:
            return jsonify({
                'status': 'error',
                'message': 'Failed to get container status',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'message': 'Docker command timed out'
        }), 500
    except json.JSONDecodeError as e:
        return jsonify({
            'status': 'error',
            'message': f'Error parsing docker output: {str(e)}'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'message': str(e)
        }), 500

@app.route('/api/containers/start/<service_name>', methods=['POST'])
def start_container(service_name):
    """Start a specific service using docker-compose"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose start for specific service (only works on stopped containers)
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'start', service_name],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'service': service_name,
                'message': f'Service {service_name} started successfully'
            })
        else:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to start service',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Start command timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500

@app.route('/api/containers/stop/<service_name>', methods=['POST'])
def stop_container(service_name):
    """Stop a specific service using docker-compose"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose stop for specific service
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'stop', service_name],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'service': service_name,
                'message': f'Service {service_name} stopped successfully'
            })
        else:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to stop service',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Stop command timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500

@app.route('/api/containers/restart/<service_name>', methods=['POST'])
def restart_container(service_name):
    """Restart a specific service using docker-compose"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose restart for specific service
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'restart', service_name],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'service': service_name,
                'message': f'Service {service_name} restarted successfully'
            })
        else:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to restart service',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Restart command timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500

@app.route('/api/containers/up/<service_name>', methods=['POST'])
def up_container(service_name):
    """Create and start a specific service using docker-compose up -d"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose up -d for specific service
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'up', '-d', service_name],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'service': service_name,
                'message': f'Service {service_name} is up and running'
            })
        else:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to bring service up',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Up command timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500

@app.route('/api/containers/down/<service_name>', methods=['POST'])
def down_container(service_name):
    """Stop and remove a specific service using docker-compose down"""
    try:
        # Get docker-compose file path from environment variable
        compose_path = os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATH environment variable not found'
            }), 404
        
        # Check if docker-compose file exists
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Run docker-compose rm -s -f for specific service (stop and remove)
        result = subprocess.run(
            ['docker-compose', '-p', 'home-server', '-f', compose_path, 'rm', '-s', '-f', service_name],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'service': service_name,
                'message': f'Service {service_name} stopped and removed'
            })
        else:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to remove service',
                'error': result.stderr
            }), 500
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Down command timed out'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500

if __name__ == '__main__':
    app.run(debug=True, host='0.0.0.0', port=5000)
