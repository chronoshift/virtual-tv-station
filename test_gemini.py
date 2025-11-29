import google.generativeai as genai
import os

# Configure with your API key from environment variable
api_key = os.environ.get("GEMINI_API_KEY")
if not api_key:
    print("Error: GEMINI_API_KEY environment variable not found.")
    exit(1)

genai.configure(api_key=api_key)

# List available models
print("Listing available models:")
for m in genai.list_models():
    if 'generateContent' in m.supported_generation_methods:
        print(m.name)

# Create a model instance
print("\nAttempting to generate content with 'gemini-3-pro-preview'...")
model = genai.GenerativeModel('gemini-3-pro-preview')

# Generate content
try:
    response = model.generate_content("Hello, Gemini!")
    print(f"\nResponse from Gemini:\n{response.text}")
except Exception as e:
    print(f"Error with gemini-3-pro-preview: {e}")
