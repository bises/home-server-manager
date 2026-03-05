from flask import Flask, jsonify, send_from_directory, request
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

@app.route('/api/compose-files', methods=['GET'])
def get_compose_files():
    """Get all configured compose files"""
    try:
        compose_paths = os.getenv('DOCKER_COMPOSE_PATHS', os.getenv('DOCKER_COMPOSE_PATH', ''))
        if not compose_paths:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATHS environment variable not found'
            }), 404
        
        # Support comma-separated paths
        path_list = [p.strip() for p in compose_paths.split(',') if p.strip()]
        
        files = []
        for path in path_list:
            if os.path.exists(path):
                # Extract name from path or compose file
                name = os.path.splitext(os.path.basename(path))[0]
                try:
                    with open(path, 'r') as f:
                        compose_data = yaml.safe_load(f)
                    project_name = compose_data.get('name', name) if compose_data else name
                    files.append({
                        'path': path,
                        'name': project_name,
                        'displayName': name.replace('-compose', '').replace('-', ' ').title()
                    })
                except:
                    files.append({'path': path, 'name': name, 'displayName': name})
        
        return jsonify({
            'status': 'success',
            'compose_files': files,
            'total': len(files)
        })
        
    except Exception as e:
        return jsonify({'status': 'error', 'message': str(e)}), 500


@app.route('/api/services', methods=['GET'])
def get_services():
    """Get all services from all docker-compose files, grouped by compose file"""
    try:
        compose_paths = os.getenv('DOCKER_COMPOSE_PATHS', os.getenv('DOCKER_COMPOSE_PATH', ''))
        if not compose_paths:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATHS environment variable not found'
            }), 404
        
        path_list = [p.strip() for p in compose_paths.split(',') if p.strip()]
        all_services = []
        
        for path in path_list:
            if not os.path.exists(path):
                continue
            
            # Get project name from compose file
            with open(path, 'r') as f:
                compose_data = yaml.safe_load(f)
            
            project_name = compose_data.get('name', os.path.splitext(os.path.basename(path))[0]) if compose_data else 'unknown'
            display_name = os.path.splitext(os.path.basename(path))[0].replace('-compose', '').replace('-', ' ').title()
            
            services = list(compose_data.get('services', {}).keys()) if compose_data else []
            
            for svc in services:
                all_services.append({
                    'service': svc,
                    'compose_file': path,
                    'project_name': project_name,
                    'display_name': display_name
                })
        
        return jsonify({
            'status': 'success',
            'services': all_services,
            'total': len(all_services)
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
    """Get all running containers from all docker-compose files"""
    try:
        compose_paths = os.getenv('DOCKER_COMPOSE_PATHS', os.getenv('DOCKER_COMPOSE_PATH', ''))
        if not compose_paths:
            return jsonify({
                'status': 'error',
                'message': 'DOCKER_COMPOSE_PATHS environment variable not found'
            }), 404
        
        path_list = [p.strip() for p in compose_paths.split(',') if p.strip()]
        all_containers = []
        
        for path in path_list:
            if not os.path.exists(path):
                continue
            
            # Get project name from compose file
            with open(path, 'r') as f:
                compose_data = yaml.safe_load(f)
            project_name = compose_data.get('name', os.path.splitext(os.path.basename(path))[0]) if compose_data else 'unknown'
            display_name = os.path.splitext(os.path.basename(path))[0].replace('-compose', '').replace('-', ' ').title()
            
            # Run docker-compose ps -a --format json with explicit project name
            result = subprocess.run(
                ['docker-compose', '-p', project_name, '-f', path, 'ps', '-a', '--format', 'json'],
                capture_output=True,
                text=True,
                timeout=30
            )
            
            if result.returncode == 0 and result.stdout.strip():
                for line in result.stdout.strip().split('\n'):
                    if line:
                        try:
                            container_data = json.loads(line)
                            all_containers.append({
                                'Name': container_data.get('Name', ''),
                                'Service': container_data.get('Service', ''),
                                'State': container_data.get('State', ''),
                                'Status': container_data.get('Status', ''),
                                'Ports': container_data.get('Publishers', []),
                                'compose_file': path,
                                'project_name': project_name,
                                'display_name': display_name
                            })
                        except json.JSONDecodeError:
                            continue
        
        return jsonify({
            'status': 'success',
            'containers': all_containers,
            'total': len(all_containers)
        })
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'message': 'Docker command timed out'
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
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'start', service_name],
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
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'stop', service_name],
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
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'restart', service_name],
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
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'up', '-d', service_name],
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
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'rm', '-s', '-f', service_name],
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

@app.route('/api/containers/pull/<service_name>', methods=['POST'])
def pull_container(service_name):
    """Pull the latest image for a service and restart it if it was running"""
    try:
        compose_path = request.args.get('compose_file') or os.getenv('DOCKER_COMPOSE_PATH')
        
        if not compose_path:
            return jsonify({
                'status': 'error',
                'message': 'compose_file parameter or DOCKER_COMPOSE_PATH not found'
            }), 404
        
        if not os.path.exists(compose_path):
            return jsonify({
                'status': 'error',
                'message': f'Docker compose file not found at: {compose_path}'
            }), 404
        
        # Get project name from compose file
        with open(compose_path, 'r') as f:
            compose_data = yaml.safe_load(f)
        project_name = compose_data.get('name', 'default') if compose_data else 'default'
        
        # Step 1: Check the current state of the container
        status_result = subprocess.run(
            ['docker-compose', '-p', project_name, '-f', compose_path, 'ps', '-a', '--format', 'json', service_name],
            capture_output=True,
            text=True,
            timeout=30
        )
        
        was_running = False
        container_exists = False
        
        if status_result.returncode == 0 and status_result.stdout.strip():
            for line in status_result.stdout.strip().split('\n'):
                if line:
                    container_data = json.loads(line)
                    if container_data.get('Service') == service_name:
                        container_exists = True
                        was_running = container_data.get('State', '').lower() == 'running'
                        break
        
        # Step 2: Pull the latest image
        pull_result = subprocess.run(
            ['-p', project_name, '-f', compose_path, 'pull', service_name],
            capture_output=True,
            text=True,
            timeout=300  # 5 minutes for pulling images
        )
        
        if pull_result.returncode != 0:
            return jsonify({
                'status': 'error',
                'service': service_name,
                'message': 'Failed to pull latest image',
                'error': pull_result.stderr
            }), 500
        
        # Check if image was actually updated
        pull_output = pull_result.stdout + pull_result.stderr
        image_updated = 'Pulled' in pull_output or 'Downloaded newer image' in pull_output or 'Digest:' in pull_output
        
        # Step 3: Handle container based on its previous state
        if was_running:
            # Container was running, so stop it, remove it, and bring it up with new image
            # Stop and remove the container
            down_result = subprocess.run(
                ['-p', project_name, '-f', compose_path, 'rm', '-s', '-f', service_name],
                capture_output=True,
                text=True,
                timeout=60
            )
            
            if down_result.returncode != 0:
                return jsonify({
                    'status': 'error',
                    'service': service_name,
                    'message': 'Image pulled but failed to stop old container',
                    'error': down_result.stderr
                }), 500
            
            # Bring up the service with new image
            up_result = subprocess.run(
                ['-p', project_name, '-f', compose_path, 'up', '-d', service_name],
                capture_output=True,
                text=True,
                timeout=60
            )
            
            if up_result.returncode != 0:
                return jsonify({
                    'status': 'error',
                    'service': service_name,
                    'message': 'Image pulled and old container removed, but failed to start new container',
                    'error': up_result.stderr
                }), 500
            
            update_msg = 'updated to latest image' if image_updated else 'already on latest image'
            return jsonify({
                'status': 'success',
                'service': service_name,
                'image_updated': image_updated,
                'message': f'Service {service_name} {update_msg} and restarted',
                'was_running': True
            })
        
        elif container_exists:
            # Container exists but was stopped, just remove it
            down_result = subprocess.run(
                ['-p', project_name, '-f', compose_path, 'rm', '-f', service_name],
                capture_output=True,
                text=True,
                timeout=60
            )
            
            if down_result.returncode != 0:
                return jsonify({
                    'status': 'error',
                    'service': service_name,
                    'message': 'Failed to remove old container',
                    'error': down_result.stderr
                }), 500
            
            update_status = '(new version available)' if image_updated else '(already up to date)'
            return jsonify({
                'status': 'success',
                'service': service_name,
                'image_updated': image_updated,
                'message': f'Latest image pulled for {service_name} {update_status}. Old container removed. Use "Bring Up" to start with new image.',
                'was_running': False
            })
        
        else:
            # Container didn't exist, just pulled the image
            update_status = '(new version available)' if image_updated else '(already up to date)'
            return jsonify({
                'status': 'success',
                'service': service_name,
                'image_updated': image_updated,
                'message': f'Latest image pulled for {service_name} {update_status}. Use "Bring Up" to start the container.',
                'was_running': False
            })
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': 'Pull command timed out (image might be large)'
        }), 500
    except json.JSONDecodeError as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': f'Error parsing container status: {str(e)}'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'service': service_name,
            'message': str(e)
        }), 500


@app.route('/api/openclaw/status', methods=['GET'])
def get_openclaw_status():
    """Get OpenClaw status and info"""
    try:
        # Try running openclaw from host via docker with host network
        # This requires openclaw to be accessible in the container or on host
        result = subprocess.run(
            ['docker', 'run', '--rm', '-i', '--network=host', 'python:3.11-slim', 'sh', '-c', 
             'pip install -q openclaw && openclaw status --json'],
            capture_output=True,
            text=True,
            timeout=60
        )
        
        if result.returncode == 0 and result.stdout.strip():
            try:
                # Find JSON in output
                import re
                json_match = re.search(r'\{.*\}', result.stdout, re.DOTALL)
                if json_match:
                    status_data = json.loads(json_match.group())
                    return jsonify({
                        'status': 'success',
                        'openclaw': status_data
                    })
            except (json.JSONDecodeError, AttributeError):
                pass
        
        # If above fails, try direct host access via curl to dashboard
        try:
            import requests
            resp = requests.get('http://host.docker.internal:18789/', timeout=5)
            if resp.status_code == 200:
                return jsonify({
                    'status': 'success',
                    'openclaw': {
                        'message': 'OpenClaw is running (web UI available)',
                        'url': 'http://host.docker.internal:18789/'
                    }
                })
        except:
            pass
            
        # OpenClaw not available
        return jsonify({
            'status': 'unavailable',
            'message': 'OpenClaw is not running or not accessible from container'
        })
            
    except subprocess.TimeoutExpired:
        return jsonify({
            'status': 'unavailable',
            'message': 'OpenClaw status check timed out'
        })
    except Exception as e:
        return jsonify({
            'status': 'unavailable',
            'message': str(e)
        })
        }), 500


@app.route('/api/openclaw/restart', methods=['POST'])
def restart_openclaw():
    """Restart OpenClaw service"""
    try:
        result = subprocess.run(
            ['sudo', 'systemctl', 'restart', 'openclaw'],
            capture_output=True,
            text=True,
            timeout=30
        )
        
        if result.returncode == 0:
            return jsonify({
                'status': 'success',
                'message': 'OpenClaw restarted successfully'
            })
        else:
            return jsonify({
                'status': 'error',
                'message': 'Failed to restart OpenClaw',
                'error': result.stderr
            }), 500
            
    except FileNotFoundError:
        return jsonify({
            'status': 'error',
            'message': 'systemctl not found'
        }), 500
    except Exception as e:
        return jsonify({
            'status': 'error',
            'message': str(e)
        }), 500


if __name__ == '__main__':
    app.run(debug=True, host='0.0.0.0', port=5000)
